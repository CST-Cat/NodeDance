package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func persistDockerNode(ctx context.Context, db *sql.DB, saved coredocker.PersistedNode, now time.Time) error {
	if db == nil || saved.Identity.AgentID == "" || saved.Identity.NodeID == "" || saved.Generation == 0 || saved.Generation > uint64(^uint64(0)>>1) {
		return errors.New("Docker persistence state is invalid")
	}
	var healthJSON any
	if saved.Health != nil {
		encoded, err := json.Marshal(saved.Health)
		if err != nil {
			return fmt.Errorf("encode Docker health: %w", err)
		}
		healthJSON = string(encoded)
	}
	healthAt := int64(0)
	if !saved.HealthReceived.IsZero() {
		healthAt = saved.HealthReceived.UnixNano()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Docker persistence transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO docker_node_state(node_id, agent_id, generation, health_json, health_received_at, stale_reason, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET agent_id=excluded.agent_id, generation=excluded.generation,
		health_json=excluded.health_json, health_received_at=excluded.health_received_at,
		stale_reason=excluded.stale_reason, updated_at=excluded.updated_at`,
		saved.Identity.NodeID, saved.Identity.AgentID, int64(saved.Generation), healthJSON, healthAt, saved.StaleReason, now.UnixNano()); err != nil {
		return fmt.Errorf("persist Docker node state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docker_containers WHERE node_id=?`, saved.Identity.NodeID); err != nil {
		return fmt.Errorf("replace Docker container inventory: %w", err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO docker_containers(node_id, container_id, record_json) VALUES(?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare Docker inventory persistence: %w", err)
	}
	defer statement.Close()
	for _, record := range saved.Containers {
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode Docker container record: %w", err)
		}
		if _, err := statement.ExecContext(ctx, saved.Identity.NodeID, record.Container.ID, string(encoded)); err != nil {
			return fmt.Errorf("persist Docker container record: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Docker persistence transaction: %w", err)
	}
	return nil
}

func persistDockerHealth(ctx context.Context, db *sql.DB, saved coredocker.PersistedNode, now time.Time) error {
	if db == nil || saved.Identity.AgentID == "" || saved.Identity.NodeID == "" || saved.Generation == 0 || saved.Generation > uint64(^uint64(0)>>1) || saved.Health == nil || saved.HealthReceived.IsZero() {
		return errors.New("Docker health persistence state is invalid")
	}
	encoded, err := json.Marshal(saved.Health)
	if err != nil {
		return fmt.Errorf("encode Docker health: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Docker health persistence transaction: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE docker_node_state SET health_json=?, health_received_at=?, stale_reason=?, updated_at=?
		WHERE node_id=? AND agent_id=? AND generation=?`, string(encoded), saved.HealthReceived.UnixNano(), saved.StaleReason,
		now.UnixNano(), saved.Identity.NodeID, saved.Identity.AgentID, int64(saved.Generation))
	if err != nil {
		return fmt.Errorf("persist Docker health keepalive: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check Docker health persistence result: %w", err)
	}
	if rows != 1 {
		return errors.New("Docker health persistence generation changed")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Docker health persistence: %w", err)
	}
	return nil
}

func loadDockerNodes(ctx context.Context, db *sql.DB) ([]coredocker.PersistedNode, error) {
	rows, err := db.QueryContext(ctx, `SELECT node_id, agent_id, generation, health_json, health_received_at, stale_reason FROM docker_node_state ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("load Docker node state: %w", err)
	}
	defer rows.Close()
	byNode := make(map[string]*coredocker.PersistedNode)
	for rows.Next() {
		var saved coredocker.PersistedNode
		var generation, healthAt int64
		var healthRaw sql.NullString
		if err := rows.Scan(&saved.Identity.NodeID, &saved.Identity.AgentID, &generation, &healthRaw, &healthAt, &saved.StaleReason); err != nil {
			return nil, fmt.Errorf("read Docker node state: %w", err)
		}
		if generation <= 0 {
			return nil, errors.New("persisted Docker generation is invalid")
		}
		saved.Generation = uint64(generation)
		if healthAt != 0 {
			saved.HealthReceived = time.Unix(0, healthAt).UTC()
		}
		if healthRaw.Valid {
			var health protocol.DockerHealth
			if err := decodePersistedJSON([]byte(healthRaw.String), &health); err != nil {
				return nil, fmt.Errorf("decode persisted Docker health: %w", err)
			}
			saved.Health = &health
		}
		byNode[saved.Identity.NodeID] = &saved
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Docker node state: %w", err)
	}
	rows.Close()
	containerRows, err := db.QueryContext(ctx, `SELECT node_id, container_id, record_json FROM docker_containers ORDER BY node_id, container_id`)
	if err != nil {
		return nil, fmt.Errorf("load Docker inventory: %w", err)
	}
	defer containerRows.Close()
	for containerRows.Next() {
		var nodeID, containerID string
		var raw []byte
		if err := containerRows.Scan(&nodeID, &containerID, &raw); err != nil {
			return nil, fmt.Errorf("read Docker inventory: %w", err)
		}
		saved := byNode[nodeID]
		if saved == nil {
			return nil, errors.New("Docker inventory references missing node state")
		}
		var record coredocker.ContainerRecord
		if err := decodePersistedJSON(raw, &record); err != nil {
			return nil, fmt.Errorf("decode persisted Docker container: %w", err)
		}
		if record.Container.ID != containerID {
			return nil, errors.New("persisted Docker container identity does not match its key")
		}
		saved.Containers = append(saved.Containers, record)
	}
	if err := containerRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Docker inventory: %w", err)
	}
	result := make([]coredocker.PersistedNode, 0, len(byNode))
	for _, saved := range byNode {
		result = append(result, *saved)
	}
	return result, nil
}

func decodePersistedJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("persisted JSON has trailing data")
		}
		return err
	}
	return nil
}
