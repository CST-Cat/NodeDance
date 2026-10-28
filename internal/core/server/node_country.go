package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
)

type nodeGeographyPreferences struct {
	locations map[string]string
	countries map[string]string
}

func (s *Server) nodeGeographyPreferences(ctx context.Context, nodeIDs []string) nodeGeographyPreferences {
	load := func(query string) map[string]string {
		values := make(map[string]string)
		for start := 0; start < len(nodeIDs); start += 500 {
			end := min(start+500, len(nodeIDs))
			ids := nodeIDs[start:end]
			args := make([]any, len(ids))
			for i, nodeID := range ids {
				args[i] = nodeID
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
			rows, err := s.store.DB.QueryContext(ctx, query+placeholders+`)`, args...)
			if err != nil {
				return nil
			}
			for rows.Next() {
				var nodeID, value string
				if err := rows.Scan(&nodeID, &value); err != nil {
					_ = rows.Close()
					return nil
				}
				values[nodeID] = value
			}
			err = rows.Err()
			if closeErr := rows.Close(); err != nil || closeErr != nil {
				return nil
			}
		}
		return values
	}
	return nodeGeographyPreferences{
		locations: load(`SELECT node_id, location_json FROM node_location_preferences WHERE node_id IN (`),
		countries: load(`SELECT node_id, country_code FROM node_country_preferences WHERE node_id IN (`),
	}
}

func (s *Server) nodeGeography(ctx context.Context, nodeID, agentID string) (geolocation.Country, geolocation.Location) {
	return s.nodeGeographyWithPreferences(ctx, nodeID, agentID, nodeGeographyPreferences{})
}

func (s *Server) nodeGeographyWithPreferences(ctx context.Context, nodeID, agentID string, prefs nodeGeographyPreferences) (geolocation.Country, geolocation.Location) {
	locationJSON := prefs.locations[nodeID]
	if prefs.locations == nil {
		_ = s.store.DB.QueryRowContext(ctx, `SELECT location_json FROM node_location_preferences WHERE node_id=?`, nodeID).Scan(&locationJSON)
	}
	if locationJSON != "" {
		var point *geolocation.Position
		if json.Unmarshal([]byte(locationJSON), &point) == nil && geolocation.ValidPosition(point) {
			return geolocation.Country{Status: "known", Code: point.CountryCode, Source: "manual"}, geolocation.Location{Status: "known", Source: "manual", Position: point}
		}
	}
	automatic := s.automaticCountry(ctx, nodeID, agentID)
	code := prefs.countries[nodeID]
	if prefs.countries == nil {
		_ = s.store.DB.QueryRowContext(ctx, `SELECT country_code FROM node_country_preferences WHERE node_id=?`, nodeID).Scan(&code)
	}
	if code != "" {
		country := geolocation.Country{Status: "known", Code: code, Source: "manual"}
		if automatic.Code == code {
			return country, geolocation.LocationFromCountry(automatic)
		}
		return country, geolocation.Location{Status: "unknown", Reason: "manual_country_only"}
	}
	return automatic, geolocation.LocationFromCountry(automatic)
}
func (s *Server) automaticCountry(ctx context.Context, nodeID, agentID string) geolocation.Country {
	// Prefer the current Agent-reported public exit IP, including for NAT nodes.
	_, view, err := s.metricsViewForNode(ctx, nodeID)
	if err == nil && view != nil && view.Metrics.Host != nil {
		for _, metric := range []struct{ value *string }{{view.Metrics.Host.PublicIPv4.Value}, {view.Metrics.Host.PublicIPv6.Value}} {
			if metric.value != nil {
				if ip := geolocation.PublicIP(*metric.value); ip != "" {
					return s.countries.Lookup(ip)
				}
			}
		}
	}
	return s.countries.Lookup(s.agents.EnrollmentIP(ctx, agentID))
}
