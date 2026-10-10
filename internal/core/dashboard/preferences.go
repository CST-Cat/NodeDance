// Package dashboard stores administrator display preferences independently of
// Docker Engine state. Keys for Compose services and standalone containers are
// supplied by the Core from their stable resource identity.
package dashboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidPreference = errors.New("dashboard preference is invalid")

func NodeIdentity(nodeID string) string { return "node:" + nodeID }

func ContainerIdentity(containerID string) (string, bool) {
	decoded, err := hex.DecodeString(containerID)
	if err != nil || len(decoded) != 32 || strings.ToLower(containerID) != containerID {
		return "", false
	}
	return "container:" + containerID, true
}

// ComposeServiceIdentity is independent of replica/container names and IDs.
// Project directory and config-file labels disambiguate two projects that use
// the same project/service names on one node; the owning node is always part
// of the hash input so preferences can never bleed across nodes.
func ComposeServiceIdentity(nodeID, project, workingDir, configFiles, service string) (string, bool) {
	if !validNodeID(nodeID) || strings.TrimSpace(project) != project || project == "" || len(project) > 256 ||
		strings.TrimSpace(service) != service || service == "" || len(service) > 256 ||
		len(workingDir) > 4096 || len(configFiles) > 4096 {
		return "", false
	}
	for _, value := range []string{project, workingDir, configFiles, service} {
		if strings.ContainsRune(value, '\x00') {
			return "", false
		}
	}
	material := strings.Join([]string{nodeID, project, workingDir, configFiles, service}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "compose:" + hex.EncodeToString(digest[:]), true
}

type Preference struct {
	NodeID     string `json:"nodeId"`
	TargetKind string `json:"targetKind"`
	Identity   string `json:"identity"`
	Alias      string `json:"alias"`
	Icon       string `json:"icon"`
	Notes      string `json:"notes"`
	ServiceURL string `json:"serviceUrl"`
	Group      string `json:"group"`
	SortOrder  int    `json:"sortOrder"`
	Visible    bool   `json:"visible"`
	Pinned     bool   `json:"pinned"`
}

type Settings struct {
	ViewMode      string   `json:"viewMode"`
	GroupBy       string   `json:"groupBy"`
	SortBy        string   `json:"sortBy"`
	NodeGroupBy   string   `json:"nodeGroupBy"`
	NodeSortBy    string   `json:"nodeSortBy"`
	FeaturedLimit int      `json:"featuredLimit"`
	CustomFields  []string `json:"customFields"`
}

type Repository struct {
	DB  *sql.DB
	Now func() time.Time
}

func (r Repository) List(ctx context.Context, nodeID string) ([]Preference, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT node_id, target_kind, identity_key, alias, icon, notes, service_url, group_name, sort_order, visible, pinned
		FROM dashboard_preferences WHERE node_id=? ORDER BY sort_order, target_kind, identity_key`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Preference, 0)
	for rows.Next() {
		var item Preference
		var visible, pinned int
		if err := rows.Scan(&item.NodeID, &item.TargetKind, &item.Identity, &item.Alias, &item.Icon,
			&item.Notes, &item.ServiceURL, &item.Group, &item.SortOrder, &visible, &pinned); err != nil {
			return nil, err
		}
		item.Visible, item.Pinned = visible == 1, pinned == 1
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r Repository) Put(ctx context.Context, item Preference) error {
	if !validPreference(item) {
		return ErrInvalidPreference
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	visible, pinned := 0, 0
	if item.Visible {
		visible = 1
	}
	if item.Pinned {
		pinned = 1
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO dashboard_preferences(node_id, target_kind, identity_key, alias, icon, notes, service_url, group_name, sort_order, visible, pinned, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, target_kind, identity_key) DO UPDATE SET alias=excluded.alias, icon=excluded.icon, notes=excluded.notes,
		service_url=excluded.service_url, group_name=excluded.group_name, sort_order=excluded.sort_order, visible=excluded.visible, pinned=excluded.pinned, updated_at=excluded.updated_at`,
		item.NodeID, item.TargetKind, item.Identity, strings.TrimSpace(item.Alias), item.Icon, strings.TrimSpace(item.Notes),
		strings.TrimSpace(item.ServiceURL), strings.TrimSpace(item.Group), item.SortOrder, visible, pinned, now.UTC().UnixNano())
	return err
}

func (r Repository) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var fields string
	err := r.DB.QueryRowContext(ctx, `SELECT view_mode, group_by, sort_by, node_group_by, node_sort_by, featured_limit, custom_fields_json FROM dashboard_settings WHERE id=1`).
		Scan(&settings.ViewMode, &settings.GroupBy, &settings.SortBy, &settings.NodeGroupBy, &settings.NodeSortBy, &settings.FeaturedLimit, &fields)
	if err != nil {
		return Settings{}, err
	}
	settings.CustomFields, err = decodeFields(fields)
	if err != nil {
		return Settings{}, fmt.Errorf("stored dashboard custom fields are invalid: %w", err)
	}
	if !validSettings(settings) {
		return Settings{}, errors.New("stored dashboard settings are invalid")
	}
	return settings, nil
}

func (r Repository) PutSettings(ctx context.Context, settings Settings) error {
	if settings.NodeGroupBy == "" {
		settings.NodeGroupBy = "status"
	}
	if settings.NodeSortBy == "" {
		settings.NodeSortBy = "custom"
	}
	if !validSettings(settings) {
		return ErrInvalidPreference
	}
	fields := encodeFields(settings.CustomFields)
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE dashboard_settings SET view_mode=?, group_by=?, sort_by=?, node_group_by=?, node_sort_by=?, featured_limit=?, custom_fields_json=?, updated_at=? WHERE id=1`,
		settings.ViewMode, settings.GroupBy, settings.SortBy, settings.NodeGroupBy, settings.NodeSortBy, settings.FeaturedLimit, fields, now.UTC().UnixNano())
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm dashboard settings update: %w", err)
	}
	if updated != 1 {
		return errors.New("dashboard settings row is missing")
	}
	return nil
}

func validPreference(item Preference) bool {
	if !validNodeID(item.NodeID) || item.Identity == "" || len(item.Identity) > 4096 || strings.TrimSpace(item.Identity) != item.Identity ||
		len(item.Alias) > 128 || len(item.Notes) > 2048 || len(item.Group) > 128 || strings.ContainsRune(item.Group, '\x00') ||
		item.SortOrder < -1_000_000 || item.SortOrder > 1_000_000 {
		return false
	}
	switch item.TargetKind {
	case "node":
		if !validNodeID(item.NodeID) || item.Identity != NodeIdentity(item.NodeID) {
			return false
		}
	case "container":
		if item.Group != "" {
			return false
		}
		if _, ok := ContainerIdentity(strings.TrimPrefix(item.Identity, "container:")); !strings.HasPrefix(item.Identity, "container:") || !ok {
			return false
		}
	case "compose_service":
		if item.Group != "" {
			return false
		}
		identity := strings.TrimPrefix(item.Identity, "compose:")
		decoded, err := hex.DecodeString(identity)
		if !strings.HasPrefix(item.Identity, "compose:") || err != nil || len(decoded) != sha256.Size {
			return false
		}
	default:
		return false
	}
	allowedIcons := map[string]struct{}{"": {}, "server": {}, "globe": {}, "database": {}, "shield": {}, "terminal": {}, "box": {}, "cloud": {}, "folder": {}, "activity": {}}
	if _, ok := allowedIcons[item.Icon]; !ok {
		return false
	}
	if item.ServiceURL == "" {
		return true
	}
	parsed, err := url.Parse(item.ServiceURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" && len(item.ServiceURL) <= 2048
}

func validNodeID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validSettings(s Settings) bool {
	if s.ViewMode != "monitor" && s.ViewMode != "manage" {
		return false
	}
	if s.NodeGroupBy != "group" && s.NodeGroupBy != "status" && s.NodeGroupBy != "none" {
		return false
	}
	if s.NodeSortBy != "custom" && s.NodeSortBy != "name" && s.NodeSortBy != "status" {
		return false
	}
	if s.GroupBy != "node" && s.GroupBy != "compose" && s.GroupBy != "state" && s.GroupBy != "none" {
		return false
	}
	if s.SortBy != "custom" && s.SortBy != "name" && s.SortBy != "state" {
		return false
	}
	if s.FeaturedLimit < 1 || s.FeaturedLimit > 20 || len(s.CustomFields) > 12 {
		return false
	}
	allowed := map[string]struct{}{"state": {}, "ports": {}, "health": {}, "uptime": {}, "image": {}}
	seen := make(map[string]struct{}, len(s.CustomFields))
	for _, field := range s.CustomFields {
		if _, ok := allowed[field]; !ok {
			return false
		}
		if _, duplicate := seen[field]; duplicate {
			return false
		}
		seen[field] = struct{}{}
	}
	return true
}

func encodeFields(fields []string) string {
	// The supported field names contain no quotes or delimiters.
	var b strings.Builder
	b.WriteByte('[')
	for i, field := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(field)
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

func decodeFields(raw string) ([]string, error) {
	var fields []string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	return fields, nil
}
