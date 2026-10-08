package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestLatePriorGenerationDockerFramesCannotStaleFreshServerInventory(t *testing.T) {
	server := newTestServer(t)
	identity := insertDockerTestNode(t, server)
	if _, err := server.store.DB.Exec(`INSERT INTO agent_devices(id, node_id, credential_digest, created_at) VALUES(?, ?, ?, ?)`,
		identity.AgentID, identity.NodeID, make([]byte, 32), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.DB.Exec(`UPDATE nodes SET status='online', connection_generation=2, last_seen_at=? WHERE id=?`, time.Now().UnixNano(), identity.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := server.bindDockerConnection(context.Background(), identity, 1); err != nil {
		t.Fatal(err)
	}
	oldID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerTestFrame(1, 1, true, []protocol.DockerChange{dockerTestUpsert(1, oldID, "old-generation")}, true)); err != nil {
		t.Fatal(err)
	}
	if err := server.bindDockerConnection(context.Background(), identity, 2); err != nil {
		t.Fatal(err)
	}
	freshID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := server.acceptDockerFrame(context.Background(), identity, 2, dockerTestFrame(1, 2, true, []protocol.DockerChange{dockerTestUpsert(1, freshID, "new-generation")}, true)); err != nil {
		t.Fatal(err)
	}
	session, _, err := installIntegrationAdmin(server)
	if err != nil {
		t.Fatal(err)
	}

	type dbInventory struct {
		containers []string
		records    []string
		generation int64
		health     string
		healthAt   int64
		stale      string
	}
	readDB := func() dbInventory {
		t.Helper()
		var saved dbInventory
		rows, err := server.store.DB.Query(`SELECT container_id, record_json FROM docker_containers WHERE node_id=? ORDER BY container_id`, identity.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, record string
			if err := rows.Scan(&id, &record); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			saved.containers = append(saved.containers, id)
			saved.records = append(saved.records, record)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err := server.store.DB.QueryRow(`SELECT generation, health_json, health_received_at, stale_reason FROM docker_node_state WHERE node_id=?`, identity.NodeID).
			Scan(&saved.generation, &saved.health, &saved.healthAt, &saved.stale); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	readAPI := func() dashboardDockerMessage {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+identity.NodeID+"/containers", nil)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("authenticated current inventory API returned %d: %s", response.Code, response.Body.String())
		}
		var message dashboardDockerMessage
		if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		return message
	}

	beforeRevision := server.docker.Revision(identity.NodeID)
	beforeDB := readDB()
	beforeAPI := readAPI()
	if beforeDB.generation != 2 || beforeDB.stale != "" || len(beforeDB.containers) != 1 || beforeDB.containers[0] != freshID {
		t.Fatalf("new generation was not durably committed fresh before stale frames: %+v", beforeDB)
	}
	if beforeAPI.Inventory == nil || !beforeAPI.Inventory.AgentOnline || beforeAPI.Inventory.DataStale || beforeAPI.Inventory.ActiveGeneration != 2 ||
		len(beforeAPI.Inventory.Containers) != 1 || beforeAPI.Inventory.Containers[0].Container.ID != freshID {
		t.Fatalf("authenticated API did not expose the new fresh inventory: %+v", beforeAPI)
	}

	lateValid := dockerTestFrame(100, 100, true, []protocol.DockerChange{dockerTestUpsert(100, oldID, "late-old-generation")}, true)
	if err := server.acceptDockerFrame(context.Background(), identity, 1, lateValid); !errors.Is(err, coredocker.ErrStaleGeneration) {
		t.Fatalf("late valid prior-generation snapshot error = %v, want stale-generation rejection", err)
	}
	invalid := protocol.DockerBatch{Sequence: 101, SnapshotID: 101, FullSnapshot: true, SnapshotFinal: true,
		Health:  &protocol.DockerHealth{Sequence: 101, Availability: protocol.DockerAvailabilityAvailable, EventsConnected: true, SnapshotFresh: true, ObservedAt: time.Now().UTC()},
		Changes: []protocol.DockerChange{dockerTestUpsert(101, oldID, "duplicate-a"), dockerTestUpsert(101, oldID, "duplicate-b")}}
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerFrame{sequence: 101, batch: invalid}); !errors.Is(err, coredocker.ErrStaleGeneration) {
		t.Fatalf("late invalid prior-generation snapshot error = %v, want generation rejection before payload validation", err)
	}

	afterDB := readDB()
	afterAPI := readAPI()
	if got := server.docker.Revision(identity.NodeID); got != beforeRevision {
		t.Fatalf("prior-generation frames changed visible revision: before=%d after=%d", beforeRevision, got)
	}
	if !reflect.DeepEqual(afterDB, beforeDB) {
		t.Fatalf("prior-generation frames changed durable SQLite inventory/state:\nbefore=%+v\nafter=%+v", beforeDB, afterDB)
	}
	if afterAPI.Inventory == nil || !afterAPI.Inventory.AgentOnline || afterAPI.Inventory.DataStale || afterAPI.Inventory.ActiveGeneration != 2 ||
		len(afterAPI.Inventory.Containers) != 1 || afterAPI.Inventory.Containers[0].Container.ID != freshID ||
		afterAPI.Inventory.Containers[0].Container.Name != "new-generation" {
		t.Fatalf("prior-generation frames changed or staled the authenticated current API inventory: %+v", afterAPI)
	}
}

func TestDockerSQLiteFailuresKeepLastCommittedFullAndIncrementalInventory(t *testing.T) {
	server := newTestServer(t)
	identity := insertDockerTestNode(t, server)
	if err := server.bindDockerConnection(context.Background(), identity, 1); err != nil {
		t.Fatal(err)
	}
	oldID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerTestFrame(1, 1, true, []protocol.DockerChange{dockerTestUpsert(1, oldID, "last-good")}, true)); err != nil {
		t.Fatal(err)
	}

	assertLastGood := func(stage string) {
		t.Helper()
		var rows int
		if err := server.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=?`, identity.NodeID).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("%s failed transaction changed committed SQLite rows: count=%d err=%v", stage, rows, err)
		}
		var persisted coredocker.ContainerRecord
		var raw string
		if err := server.store.DB.QueryRow(`SELECT record_json FROM docker_containers WHERE node_id=? AND container_id=?`, identity.NodeID, oldID).Scan(&raw); err != nil {
			t.Fatalf("%s lost the previous persisted container: %v", stage, err)
		}
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil || persisted.Container.Name != "last-good" {
			t.Fatalf("%s changed the previous persisted record: %+v err=%v", stage, persisted, err)
		}
		lease := &coredocker.Lease{Identity: identity, Generation: 1, ValidUntil: time.Now().Add(time.Hour), Status: coredocker.LeaseOnline}
		view, ok := server.dockerSnapshot(identity.NodeID, lease, time.Now())
		if !ok || len(view.Containers) != 1 || view.Containers[0].Container.Name != "last-good" || !view.DataStale {
			t.Fatalf("%s published uncommitted inventory or hid stale status: %+v", stage, view)
		}
	}

	newIncremental := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := server.store.DB.Exec(`CREATE TRIGGER fail_incremental_docker BEFORE INSERT ON docker_containers WHEN NEW.container_id='` + newIncremental + `' BEGIN SELECT RAISE(ABORT, 'injected incremental write failure'); END`); err != nil {
		t.Fatal(err)
	}
	incremental := protocol.DockerBatch{Sequence: 2, Changes: []protocol.DockerChange{dockerTestUpsert(2, newIncremental, "must-not-appear")}}
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerFrame{sequence: 2, batch: incremental}); err == nil {
		t.Fatal("incremental persistence failure was reported as success")
	}
	assertLastGood("incremental")

	if _, err := server.store.DB.Exec(`DROP TRIGGER fail_incremental_docker`); err != nil {
		t.Fatal(err)
	}
	newFull := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := server.store.DB.Exec(`CREATE TRIGGER fail_full_docker BEFORE INSERT ON docker_containers WHEN NEW.container_id='` + newFull + `' BEGIN SELECT RAISE(ABORT, 'injected full snapshot write failure'); END`); err != nil {
		t.Fatal(err)
	}
	full := dockerTestFrame(3, 2, true, []protocol.DockerChange{dockerTestUpsert(3, newFull, "must-not-appear")}, true)
	if err := server.acceptDockerFrame(context.Background(), identity, 1, full); err == nil {
		t.Fatal("full-snapshot persistence failure was reported as success")
	}
	assertLastGood("full snapshot")
}

func TestDockerHealthKeepaliveUpdatesSQLiteWithoutRewritingInventory(t *testing.T) {
	clock := time.Date(2026, 10, 8, 19, 0, 0, 0, time.UTC)
	server, err := New("docker-health-keepalive", Options{DataDir: filepath.Join(t.TempDir(), "core"), Development: true, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	identity := insertDockerTestNode(t, server)
	if err := server.bindDockerConnection(context.Background(), identity, 1); err != nil {
		t.Fatal(err)
	}
	containerID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerTestFrame(1, 1, true, []protocol.DockerChange{dockerTestUpsert(1, containerID, "steady")}, true)); err != nil {
		t.Fatal(err)
	}
	revision := server.docker.Revision(identity.NodeID)
	var beforeReceived int64
	if err := server.store.DB.QueryRow(`SELECT health_received_at FROM docker_node_state WHERE node_id=?`, identity.NodeID).Scan(&beforeReceived); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.DB.Exec(`CREATE TRIGGER reject_keepalive_inventory_rewrite BEFORE DELETE ON docker_containers BEGIN SELECT RAISE(ABORT, 'health keepalive rewrote inventory'); END`); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Second)
	lastSuccess, lastSnapshot := clock, clock.Add(-time.Second)
	health := protocol.DockerHealth{Sequence: 2, Availability: protocol.DockerAvailabilityAvailable, EventsConnected: true,
		SnapshotFresh: true, LastSuccessAt: &lastSuccess, LastSnapshotAt: &lastSnapshot, ObservedAt: clock}
	batch := protocol.DockerBatch{Sequence: 2, Health: &health}
	if err := server.acceptDockerFrame(context.Background(), identity, 1, dockerFrame{sequence: 2, batch: batch}); err != nil {
		t.Fatalf("persist healthy Docker keepalive without rewriting container rows: %v", err)
	}
	if got := server.docker.Revision(identity.NodeID); got != revision {
		t.Fatalf("health keepalive changed visible inventory revision: before=%d after=%d", revision, got)
	}
	var afterReceived int64
	var savedHealth string
	if err := server.store.DB.QueryRow(`SELECT health_received_at, health_json FROM docker_node_state WHERE node_id=?`, identity.NodeID).Scan(&afterReceived, &savedHealth); err != nil {
		t.Fatal(err)
	}
	if afterReceived <= beforeReceived {
		t.Fatalf("SQLite Docker receive-time freshness did not advance: before=%d after=%d", beforeReceived, afterReceived)
	}
	var persisted protocol.DockerHealth
	if err := json.Unmarshal([]byte(savedHealth), &persisted); err != nil || persisted.Sequence != 2 {
		t.Fatalf("SQLite Docker health watermark was not refreshed: %+v err=%v", persisted, err)
	}
	var rows int
	if err := server.store.DB.QueryRow(`SELECT count(*) FROM docker_containers WHERE node_id=? AND container_id=?`, identity.NodeID, containerID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("health keepalive changed the committed inventory: rows=%d err=%v", rows, err)
	}
}

func TestDockerCoreRestartHydratesLastInventoryStale(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "core")
	first, err := New("docker-restart", Options{DataDir: dataDir, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	identity := insertDockerTestNode(t, first)
	if err := first.bindDockerConnection(context.Background(), identity, 1); err != nil {
		t.Fatal(err)
	}
	containerID := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err := first.acceptDockerFrame(context.Background(), identity, 1, dockerTestFrame(1, 1, true, []protocol.DockerChange{dockerTestUpsert(1, containerID, "persisted")}, true)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New("docker-restart", Options{DataDir: dataDir, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	view, ok := restarted.dockerSnapshot(identity.NodeID, nil, time.Now())
	if !ok || view.AgentOnline || !view.DataStale || len(view.Containers) != 1 || view.Containers[0].Container.Name != "persisted" || !view.Containers[0].Container.Stale {
		t.Fatalf("Core restart lost inventory or claimed it was live: %+v", view)
	}
}

func TestDockerInventoryAPIRequiresSessionAndNoTouchClassification(t *testing.T) {
	server := newTestServer(t)
	nodeID := "00000000-0000-4000-8000-000000000002"
	for _, path := range []string{
		"/api/v1/nodes/" + nodeID + "/containers",
		"/api/v1/nodes/" + nodeID + "/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("private Docker inventory %s returned %d without a browser Session", path, response.Code)
		}
		if !isMetricsTelemetryRead(request) {
			t.Fatalf("background Docker read %s is classified as activity-touching", path)
		}
	}
}

func insertDockerTestNode(t *testing.T, server *Server) coredocker.Identity {
	t.Helper()
	identity := coredocker.Identity{
		AgentID: "00000000-0000-4000-8000-000000000010",
		NodeID:  "00000000-0000-4000-8000-000000000020",
	}
	_, err := server.store.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at) VALUES(?, 'Docker test node', 'offline', ?, ?)`, identity.NodeID, time.Now().UnixNano(), time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func dockerTestFrame(sequence, snapshotID uint64, final bool, changes []protocol.DockerChange, snapshot bool) dockerFrame {
	batch := protocol.DockerBatch{Sequence: sequence, Changes: changes}
	if snapshot {
		batch.SnapshotID = snapshotID
		batch.FullSnapshot = true
		batch.SnapshotFinal = final
		batch.Health = &protocol.DockerHealth{Sequence: sequence, Availability: protocol.DockerAvailabilityAvailable,
			EventsConnected: true, SnapshotFresh: true, ObservedAt: time.Now().UTC()}
	}
	return dockerFrame{sequence: sequence, batch: batch}
}

func dockerTestUpsert(sequence uint64, containerID, name string) protocol.DockerChange {
	now := time.Now().UTC()
	return protocol.DockerChange{
		Sequence: sequence, Action: protocol.DockerChangeUpsert, ContainerID: containerID, ObservedAt: now,
		Container: &protocol.DockerContainer{ID: containerID, Name: name, Image: "test-image@sha256:fixture", ImageID: "sha256:fixture",
			State: "running", Running: true, Health: protocol.DockerHealthNone, ObservedAt: now},
	}
}
