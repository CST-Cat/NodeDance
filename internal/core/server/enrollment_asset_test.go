package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestEnrollmentAssetsPersistThroughRegistrationAndRenewal(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	for _, unit := range []string{"year", "month", "hour", "minute"} {
		asset := protocol.DefaultNodeAsset()
		asset.BillingCycleUnit = unit
		asset.Price = 12.5
		asset.TrafficLimitBytes = 1000000000000
		expiry := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
		asset.ExpiresAt = &expiry
		purchased := expiry.AddDate(-1, 0, 0)
		asset.PurchasedAt = &purchased
		e, err := s.agents.CreateEnrollment(ctx, "asset-"+unit, "test", sql.NullInt64{}, asset)
		if err != nil {
			t.Fatal(err)
		}
		asset.Price = 22.5
		e, err = s.agents.RenewEnrollment(ctx, e.NodeID, "test", sql.NullInt64{}, asset)
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(e.Token), auth.DigestToken("asset-device-"+unit), "request-"+unit, "test")
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.nodeAsset(ctx, id.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Price != 22.5 || got.BillingCycleUnit != unit || got.TrafficLimitBytes != asset.TrafficLimitBytes || got.PurchasedAt == nil || !got.PurchasedAt.Equal(purchased) || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiry) {
			t.Fatalf("asset lost: %+v", got)
		}
	}
	var before, after int
	s.store.DB.QueryRow(`SELECT count(*) FROM nodes`).Scan(&before)
	invalid := protocol.DefaultNodeAsset()
	invalid.BillingCycleUnit = "second"
	if _, err := s.agents.CreateEnrollment(ctx, "invalid", "test", sql.NullInt64{}, invalid); err == nil {
		t.Fatal("invalid asset accepted")
	}
	s.store.DB.QueryRow(`SELECT count(*) FROM nodes`).Scan(&after)
	if before != after {
		t.Fatal("invalid asset created pending node")
	}
}
