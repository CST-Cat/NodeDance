package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

func TestRenewEnrollmentPreservesNodeAndInvalidatesPreviousCommand(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	original, err := s.agents.CreateEnrollment(ctx, "pending node", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	renew := func() agents.Enrollment {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+original.NodeID+"/enrollment", strings.NewReader(`{"coreUrl":"https://core.example","deploymentMethod":"docker"}`))
		res := httptest.NewRecorder()
		s.adminCreateAgentEnrollment(res, req, nil)
		if res.Code != 201 {
			t.Fatalf("renew status=%d %s", res.Code, res.Body.String())
		}
		var body struct {
			NodeID  string `json:"nodeId"`
			Token   string `json:"token"`
			Command string `json:"installCommand"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.NodeID != original.NodeID || !strings.Contains(body.Command, "--token "+body.Token) || !strings.Contains(body.Command, "--mode docker") {
			t.Fatal("wrong renewed command")
		}
		return agents.Enrollment{NodeID: body.NodeID, Token: body.Token}
	}
	first := renew()
	second := renew()
	for _, stale := range []agents.Enrollment{original, first} {
		_, err = s.agents.ConsumeEnrollment(ctx, auth.DigestToken(stale.Token), auth.DigestToken(strings.Repeat("a", 64)), "stale", "test")
		if !errors.Is(err, agents.ErrEnrollmentRejected) {
			t.Fatalf("old token accepted: %v", err)
		}
	}
	nodes, err := s.agents.ListNodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("renew created duplicate nodes: %v", err)
	}
	_, err = s.agents.ConsumeEnrollment(ctx, auth.DigestToken(second.Token), auth.DigestToken(strings.Repeat("b", 64)), "latest", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.agents.RenewEnrollment(ctx, original.NodeID, "test", sql.NullInt64{}); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("registered renewal: %v", err)
	}
}

func TestDeleteNodeEveryStateInvalidatesCredentialsAndCleansDependencies(t *testing.T) {
	for _, status := range []string{"pending", "online", "offline", "revoked"} {
		t.Run(status, func(t *testing.T) {
			s := featureCore(t)
			ctx := context.Background()
			e, err := s.agents.CreateEnrollment(ctx, "delete fixture", "test", sql.NullInt64{})
			if err != nil {
				t.Fatal(err)
			}
			deviceToken := strings.Repeat("c", 64)
			if status != "pending" {
				if _, err = s.agents.ConsumeEnrollment(ctx, auth.DigestToken(e.Token), auth.DigestToken(deviceToken), "fixture", "test"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.store.DB.Exec(`UPDATE nodes SET status=? WHERE id=?`, status, e.NodeID); err != nil {
				t.Fatal(err)
			}
			// Exercise non-cascading relationships and the unresolved-task guard.
			queries := []string{
				`INSERT INTO node_assets(node_id,settings_json,updated_at) VALUES(?,'{}',1)`,
				`INSERT INTO service_probes(id,node_id,name,kind,target,interval_seconds,timeout_seconds,enabled,revision,status,next_due_at,created_at,updated_at) VALUES('probe',?,'probe','tcp','example.com:80',60,1,1,1,'unknown',1,1,1)`,
				`INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,kind,status,checked_at,timeout_seconds) VALUES('run','probe',?,1,'tcp','pending',1,1)`,
				`INSERT INTO alert_rules(id,name,kind,node_id,severity,duration_seconds,cooldown_seconds,enabled,revision,created_at,updated_at) VALUES('rule','rule','node_offline',?,'info',0,0,1,1,1,1)`,
				`INSERT INTO core_tasks(task_id,node_id,idempotency_key,request_digest,accepted_generation,target_id,resource_key,action,intent_json,status,created_at_ns,updated_at_ns) VALUES('task',?,'intent',zeroblob(32),0,'resource','resource','stop','{}','running',1,1)`,
				`INSERT INTO core_task_resource_claims(node_id,resource_key,task_id) VALUES(?,'resource','task')`,
				`INSERT INTO core_task_audit_events(node_id,task_id,event,occurred_at_ns) VALUES(?,'task','accepted',1)`,
			}
			for _, query := range queries {
				if _, err = s.store.DB.Exec(query, e.NodeID); err != nil {
					t.Fatal(err)
				}
			}
			call := func(body string) *httptest.ResponseRecorder {
				res := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/"+e.NodeID, strings.NewReader(body))
				s.adminDeleteNode(res, req, nil, e.NodeID)
				return res
			}
			if res := call(`{"confirm":false}`); res.Code != 400 {
				t.Fatal("deletion needs Core confirmation")
			}
			if res := call(`{"confirm":true}`); res.Code != 200 {
				t.Fatalf("delete %d %s", res.Code, res.Body.String())
			}
			nodes, err := s.agents.ListNodes(ctx)
			if err != nil || len(nodes) != 0 {
				t.Fatalf("node remains %v", err)
			}
			for _, table := range []string{"agent_devices", "agent_enrollments", "agent_credential_verifiers", "service_probes", "service_probe_runs", "alert_rules", "node_assets", "core_tasks", "core_task_resource_claims", "core_task_audit_events"} {
				var count int
				if err = s.store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s retains data: %d %v", table, count, err)
				}
			}
			if _, err = s.agents.ConsumeEnrollment(ctx, auth.DigestToken(e.Token), auth.DigestToken(deviceToken), "deleted", "test"); !errors.Is(err, agents.ErrEnrollmentRejected) {
				t.Fatalf("deleted enrollment accepted %v", err)
			}
			if _, err = s.agents.IdentityByCredential(ctx, auth.DigestToken(deviceToken)); err == nil {
				t.Fatal("deleted device accepted")
			}
			if res := call(`{"confirm":true}`); res.Code != 404 {
				t.Fatal("missing node should be 404")
			}
		})
	}
}

func TestNodeDeletionRollsBackOnCleanupFailure(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	e, err := s.agents.CreateEnrollment(ctx, "retained node", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	// Force a failure after dependent enrollment rows have been removed.
	if _, err = s.store.DB.Exec(`CREATE TRIGGER reject_node_deletion BEFORE DELETE ON nodes BEGIN SELECT RAISE(ABORT,'fixture cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.agents.DeleteNode(ctx, e.NodeID, "test", sql.NullInt64{}); err == nil {
		t.Fatal("expected atomic cleanup failure")
	}
	nodes, err := s.agents.ListNodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatal("failed deletion lost node")
	}
	if _, err = s.agents.ConsumeEnrollment(ctx, auth.DigestToken(e.Token), auth.DigestToken(strings.Repeat("f", 64)), "retained", "test"); err != nil {
		t.Fatalf("failed deletion lost enrollment: %v", err)
	}
}
