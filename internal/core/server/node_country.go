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
	prefs := nodeGeographyPreferences{locations: map[string]string{}, countries: map[string]string{}}
	if len(nodeIDs) == 0 {
		return prefs
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(nodeIDs)), ",")
	args := make([]any, len(nodeIDs))
	for i, nodeID := range nodeIDs {
		args[i] = nodeID
	}
	if rows, err := s.store.DB.QueryContext(ctx, `SELECT node_id, location_json FROM node_location_preferences WHERE node_id IN (`+placeholders+`)`, args...); err == nil {
		for rows.Next() {
			var nodeID, locationJSON string
			if rows.Scan(&nodeID, &locationJSON) == nil {
				prefs.locations[nodeID] = locationJSON
			}
		}
		rows.Close()
	}
	if rows, err := s.store.DB.QueryContext(ctx, `SELECT node_id, country_code FROM node_country_preferences WHERE node_id IN (`+placeholders+`)`, args...); err == nil {
		for rows.Next() {
			var nodeID, code string
			if rows.Scan(&nodeID, &code) == nil {
				prefs.countries[nodeID] = code
			}
		}
		rows.Close()
	}
	return prefs
}

func (s *Server) nodeGeography(ctx context.Context, nodeID, agentID string) (geolocation.Country, geolocation.Location) {
	return s.nodeGeographyWithPreferences(ctx, nodeID, agentID, nodeGeographyPreferences{})
}

func (s *Server) nodeGeographyWithPreferences(ctx context.Context, nodeID, agentID string, prefs nodeGeographyPreferences) (geolocation.Country, geolocation.Location) {
	locationJSON, prefetched := prefs.locations[nodeID]
	if !prefetched {
		_ = s.store.DB.QueryRowContext(ctx, `SELECT location_json FROM node_location_preferences WHERE node_id=?`, nodeID).Scan(&locationJSON)
	}
	if locationJSON != "" {
		var point *geolocation.Position
		if json.Unmarshal([]byte(locationJSON), &point) == nil && geolocation.ValidPosition(point) {
			return geolocation.Country{Status: "known", Code: point.CountryCode, Source: "manual"}, geolocation.Location{Status: "known", Source: "manual", Position: point}
		}
	}
	automatic := s.automaticCountry(ctx, nodeID, agentID)
	code, prefetched := prefs.countries[nodeID]
	if !prefetched {
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
