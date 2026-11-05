package server

import (
	"context"
	"database/sql"
	"testing"

	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

func punchSeedCredentialForTest(t *testing.T, core *Server) string {
	t.Helper()
	ctx := context.Background()
	enrollment, err := core.agents.CreateEnrollment(ctx, "line-type-seed", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := core.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return identity.NodeID
}

func TestSeedNodeLineTypeStoresDetectedLabel(t *testing.T) {
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	node := punchSeedCredentialForTest(t, core)
	core.seedNodeLineType(context.Background(), node, "kvm")
	asset, err := core.nodeAsset(context.Background(), node)
	if err != nil || asset.LineType != "kvm" {
		t.Fatalf("seeded asset = %+v err=%v", asset, err)
	}
	core.seedNodeLineType(context.Background(), node, "homelab")
	again, err := core.nodeAsset(context.Background(), node)
	if err != nil || again.LineType != "kvm" {
		t.Fatalf("manual-set label was overwritten: %+v", again)
	}
	core.seedNodeLineType(context.Background(), node, "")
	if _, err := core.nodeAsset(context.Background(), node); err != nil {
		t.Fatalf("empty label rejected: %v", err)
	}
}

func TestSeedNodeLineTypeRejectsUnknown(t *testing.T) {
	core := &Server{}
	core.seedNodeLineType(context.Background(), "node-x", "provider-x")
}
