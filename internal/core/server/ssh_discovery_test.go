package server

import (
	"context"
	"database/sql"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/sshconnection"
	"testing"
	"time"
)

func TestSSHDiscoveryUsesMachineDataAndOptionalSharedOverrides(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	e, err := s.agents.CreateEnrollment(ctx, "SSH", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	info := sshconnection.Info{Status: "available", Source: "discovered", SampledAt: time.Now(), Hosts: []string{"100.64.0.8"}, Ports: []int{2222}, Users: []string{"detected"}, Host: "100.64.0.8", Port: 2222, User: "detected"}
	got, err := s.resolveNodeSSH(ctx, e.NodeID, info)
	if err != nil || got.Command != "ssh -p 2222 detected@100.64.0.8" {
		t.Fatal(got, err)
	}
	repo := dashboard.Repository{DB: s.store.DB, Now: s.now}
	pref := dashboard.Preference{NodeID: e.NodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(e.NodeID), Visible: true, SSHPort: 22, Alias: "custom display"}
	if err := repo.Put(ctx, pref); err != nil {
		t.Fatal(err)
	}
	got, err = s.resolveNodeSSH(ctx, e.NodeID, info)
	if err != nil || got.Port != 2222 {
		t.Fatal("display-only preference overrode real SSH port", got, err)
	}
	pref.SSHHost = "override.example"
	pref.SSHUser = "override_user"
	pref.SSHPort = 2200
	if err := repo.Put(ctx, pref); err != nil {
		t.Fatal(err)
	}
	got, err = s.resolveNodeSSH(ctx, e.NodeID, info)
	if err != nil || got.Command != "ssh -p 2200 override_user@override.example" {
		t.Fatal(got, err)
	}
	info.User = "bad;id"
	if _, err = s.resolveNodeSSH(ctx, e.NodeID, info); err == nil {
		t.Fatal("unsafe machine data bypassed validation")
	}
}
