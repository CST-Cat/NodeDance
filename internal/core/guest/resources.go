package guest

import (
	"context"
	"strings"
)

// AvailableResources narrows the saved publication to resources that still
// exist and belong to an enrolled node. It does not modify the saved policy.
// Public reads must call this even when their metric snapshot is cached.
func (s Store) AvailableResources(ctx context.Context, settings Settings) (Settings, error) {
	filter := func(selected []Resource, query string) ([]Resource, error) {
		out := []Resource{}
		if len(selected) == 0 {
			return out, nil
		}
		args := make([]any, len(selected))
		for i, resource := range selected {
			args[i] = resource.ID
		}
		query += "(" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
		rows, err := s.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		available := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			available[id] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, resource := range selected {
			if available[resource.ID] {
				out = append(out, resource)
			}
		}
		return out, nil
	}
	var err error
	settings.Nodes, err = filter(settings.Nodes, "SELECT id FROM nodes WHERE status NOT IN ('pending','revoked') AND id IN ")
	if err != nil {
		return Settings{}, err
	}
	settings.Probes, err = filter(settings.Probes, "SELECT p.id FROM service_probes p JOIN nodes n ON n.id=p.node_id WHERE p.deleted_at IS NULL AND n.status NOT IN ('pending','revoked') AND p.id IN ")
	return settings, err
}
