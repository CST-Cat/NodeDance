package server

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
)

func (s *Server) nodeGeography(ctx context.Context, nodeID, agentID string) (geolocation.Country, geolocation.Location) {
	var locationJSON string
	if err := s.store.DB.QueryRowContext(ctx, `SELECT location_json FROM node_location_preferences WHERE node_id=?`, nodeID).Scan(&locationJSON); err == nil {
		var point *geolocation.Position
		if json.Unmarshal([]byte(locationJSON), &point) == nil && geolocation.ValidPosition(point) {
			return geolocation.Country{Status: "known", Code: point.CountryCode, Source: "manual"}, geolocation.Location{Status: "known", Source: "manual", Position: point}
		}
	}
	automatic := s.automaticCountry(ctx, nodeID, agentID)
	var code string
	if err := s.store.DB.QueryRowContext(ctx, `SELECT country_code FROM node_country_preferences WHERE node_id=?`, nodeID).Scan(&code); err == nil && code != "" {
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
