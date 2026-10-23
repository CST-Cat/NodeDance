package guest

import (
	"context"
	"database/sql"
	"strings"
)

type resourceReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s Store) AvailableResources(ctx context.Context, settings Settings) (Settings, error) {
	return availableResources(ctx, s.DB, settings)
}

func availableResources(ctx context.Context, reader resourceReader, settings Settings) (Settings, error) {
	if len(settings.Nodes)+len(settings.Probes) == 0 {
		settings.Nodes, settings.Probes = []Resource{}, []Resource{}
		return settings, nil
	}
	var args []any
	placeholders := func(resources []Resource) string {
		if len(resources) == 0 {
			return "NULL"
		}
		for _, resource := range resources {
			args = append(args, resource.ID)
		}
		return strings.TrimSuffix(strings.Repeat("?,", len(resources)), ",")
	}
	query := "SELECT 'node',id FROM nodes WHERE status NOT IN ('pending','revoked') AND id IN (" + placeholders(settings.Nodes) + ") UNION ALL " +
		"SELECT 'probe',p.id FROM service_probes p JOIN nodes n ON n.id=p.node_id WHERE p.deleted_at IS NULL AND n.status NOT IN ('pending','revoked') AND p.id IN (" + placeholders(settings.Probes) + ")"
	rows, err := reader.QueryContext(ctx, query, args...)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return Settings{}, err
		}
		available[kind+":"+id] = true
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	filter := func(kind string, selected []Resource) []Resource {
		out := []Resource{}
		for _, resource := range selected {
			if available[kind+":"+resource.ID] {
				out = append(out, resource)
			}
		}
		return out
	}
	settings.Nodes, settings.Probes = filter("node", settings.Nodes), filter("probe", settings.Probes)
	return settings, nil
}
