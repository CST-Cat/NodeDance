package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
	"reflect"
	"testing"
)

func TestNodeCityPreferencePersistenceIsolationAndReset(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	first := guestFixtureNode(t, s, 71)
	second := guestFixtureNode(t, s, 72)
	lat, lng := 34.0522, -118.2437
	point := &geolocation.Position{CountryCode: "US", City: "Los Angeles", Region: "California", Latitude: &lat, Longitude: &lng}
	preference := dashboard.Preference{NodeID: first.NodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(first.NodeID), CountryCode: "US", Location: point, Visible: true}
	if err := s.dashboardPreferences.Put(ctx, preference); err != nil {
		t.Fatal(err)
	}
	country, location := s.nodeGeography(ctx, first.NodeID, first.AgentID)
	if country.Code != "US" || country.Source != "manual" || location.Status != "known" || location.Position.City != "Los Angeles" {
		t.Fatal(country, location)
	}
	ids := []string{first.NodeID, second.NodeID}
	for i := 0; i < 1000; i++ {
		ids = append(ids, fmt.Sprintf("absent-%d", i))
	}
	prefetched := s.nodeGeographyPreferences(ctx, ids)
	if prefetched.locations == nil || prefetched.countries == nil {
		t.Fatal("batch preference read failed")
	}
	for _, node := range []struct{ nodeID, agentID string }{{first.NodeID, first.AgentID}, {second.NodeID, second.AgentID}} {
		wantCountry, wantLocation := s.nodeGeography(ctx, node.nodeID, node.agentID)
		gotCountry, gotLocation := s.nodeGeographyWithPreferences(ctx, node.nodeID, node.agentID, prefetched)
		if !reflect.DeepEqual(gotCountry, wantCountry) || !reflect.DeepEqual(gotLocation, wantLocation) {
			t.Fatalf("batched geography differs: %v %v", gotCountry, gotLocation)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	failed := s.nodeGeographyPreferences(canceled, ids[:2])
	if failed.locations != nil || failed.countries != nil {
		t.Fatal("failed batch must fall back to individual reads")
	}
	fallbackCountry, fallbackLocation := s.nodeGeographyWithPreferences(ctx, first.NodeID, first.AgentID, failed)
	if !reflect.DeepEqual(fallbackCountry, country) || !reflect.DeepEqual(fallbackLocation, location) {
		t.Fatal("failed batch changed geography", fallbackCountry, fallbackLocation)
	}
	_, other := s.nodeGeography(ctx, second.NodeID, second.AgentID)
	if other.Status == "known" {
		t.Fatal("location crossed node identity", other)
	}
	stored, err := s.dashboardPreferences.List(ctx, first.NodeID)
	if err != nil || len(stored) != 1 || stored[0].Location == nil || *stored[0].Location.Longitude != lng {
		t.Fatal(stored, err)
	}
	invalid := preference
	invalid.CountryCode = "CN"
	if err = s.dashboardPreferences.Put(ctx, invalid); !errors.Is(err, dashboard.ErrInvalidPreference) {
		t.Fatal("conflicting country accepted", err)
	}
	bad := *point
	bad.Latitude = nil
	invalid = preference
	invalid.Location = &bad
	if err = s.dashboardPreferences.Put(ctx, invalid); !errors.Is(err, dashboard.ErrInvalidPreference) {
		t.Fatal("missing coordinate silently became zero", err)
	}
	country, location = s.nodeGeography(ctx, first.NodeID, first.AgentID)
	if country.Code != "US" || location.Position.City != "Los Angeles" {
		t.Fatal("invalid edit partially persisted")
	}
	preference.Location = nil
	preference.CountryCode = ""
	if err = s.dashboardPreferences.Put(ctx, preference); err != nil {
		t.Fatal(err)
	}
	_, location = s.nodeGeography(ctx, first.NodeID, first.AgentID)
	if location.Status == "known" || location.Reason != "public_ip_unavailable" {
		t.Fatal("reset did not restore automatic lookup", location)
	}
}
