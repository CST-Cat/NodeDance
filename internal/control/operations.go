// Package control is the Core operation contract shared by Web, CLI and TUI.
// It uses the Core's public API; it never opens its database or contacts Agents directly.
package control

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/feeds"
	"github.com/CST-Cat/NodeDance/internal/core/guest"
	coretailscale "github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
)

type Catalog struct {
	Version    int         `json:"version"`
	Operations []Operation `json:"operations"`
}

func Describe() Catalog { return Catalog{Version: 1, Operations: Operations()} }

type Operation struct {
	RequiredRole     string              `json:"requiredRole"`
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Method           string              `json:"method"`
	Path             string              `json:"path"`
	Parameters       []string            `json:"parameters"`
	Query            []string            `json:"query"`
	BodySchema       any                 `json:"bodySchema,omitempty"`
	Transport        string              `json:"transport"` // json, download, upload, multipart, websocket, local
	Idempotent       bool                `json:"requiresIdempotencyKey,omitempty"`
	Headers          map[string]any      `json:"headers,omitempty"`
	Upload           *UploadContract     `json:"upload,omitempty"`
	MaxResponseBytes int64               `json:"maxResponseBytes,omitempty"`
	MaxBodyBytes     int64               `json:"maxBodyBytes,omitempty"`
	ResponseSchema   any                 `json:"responseSchema,omitempty"`
	Completion       *CompletionContract `json:"completion,omitempty"`
	Duplex           bool                `json:"duplex,omitempty"`
}

type UploadContract struct {
	Field       string `json:"field,omitempty"`
	ContentType string `json:"contentType"`
	MaxBytes    int64  `json:"maxBytes,omitempty"`
}

// References read submitted input or returned data, never client-owned routes.
type CompletionContract struct {
	Operation        string            `json:"operation"`
	Params           map[string]string `json:"params"`
	Query            map[string]string `json:"query,omitempty"`
	StatusPath       string            `json:"statusPath"`
	TerminalStatuses []string          `json:"terminalStatuses"`
	SuccessStatuses  []string          `json:"successStatuses"`
}

func op(name, method, path, description string, body any, query ...string) Operation {
	params := []string{}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "{") {
			params = append(params, strings.Trim(part, "{}"))
		}
	}
	return Operation{Name: name, Method: method, Path: path, Description: description, Parameters: params, Query: append([]string{}, query...), BodySchema: body, Transport: "json"}
}
func fields(spec string) any {
	props := map[string]any{}
	required := []string{}
	for _, f := range strings.Fields(spec) {
		pair := strings.SplitN(f, ":", 2)
		name := strings.TrimSuffix(pair[0], "?")
		props[name] = map[string]any{"type": pair[1]}
		if name == pair[0] {
			required = append(required, name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func schema(value any) any {
	body := typeSchema(reflect.TypeOf(value)).(map[string]any)
	props, _ := body["properties"].(map[string]any)
	enum := func(key string, values ...string) {
		if p, ok := props[key].(map[string]any); ok {
			p["enum"] = values
		}
	}
	switch value.(type) {
	case protocol.AccountCreateRequest, protocol.AccountUpdateRequest:
		enum("role", "administrator", "viewer")
	case protocol.ProfileRequest:
		enum("theme", "system", "light", "dark")
	case feeds.Input:
		for _, key := range []string{"intervalMinutes", "digestMinutes"} {
			props[key].(map[string]any)["minimum"] = 1
			props[key].(map[string]any)["maximum"] = 10080
		}
		props["intervalMinutes"].(map[string]any)["default"] = 30
		props["digestMinutes"].(map[string]any)["default"] = 60
		enum("notificationMode", "off", "immediate", "digest")
		props["name"].(map[string]any)["minLength"] = 1
		props["name"].(map[string]any)["maxBytes"] = 120
		props["url"].(map[string]any)["maxBytes"] = 2048
	case guest.Settings:
		props["title"].(map[string]any)["minLength"] = 1
		props["title"].(map[string]any)["maxLength"] = 80
		props["revision"].(map[string]any)["minimum"] = 0
		for _, key := range []string{"nodes", "probes"} {
			name := props[key].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)
			name["minLength"] = 1
			name["maxLength"] = 80
		}
		props["metrics"].(map[string]any)["items"].(map[string]any)["enum"] = func() []string {
			keys := []string{}
			for _, m := range guest.Metrics() {
				keys = append(keys, m.Key)
			}
			return keys
		}()
		props["historyHours"].(map[string]any)["enum"] = []any{0, 1, 6, 24}
		for _, key := range []string{"nodes", "probes"} {
			props[key].(map[string]any)["maxItems"] = 20
		}
	case protocol.AgentSettings:
		raw, _ := json.Marshal(protocol.DefaultAgentSettings())
		var defaults map[string]any
		json.Unmarshal(raw, &defaults)
		for key, value := range defaults {
			if field, ok := props[key].(map[string]any); ok {
				field["default"] = value
			}
		}
		enum("memoryMode", "available", "raw", "cached")
		enum("transport", "auto", "websocket", "http")
		enum("preferIpVersion", "", "4", "6")
	case protocol.NodeAsset:
		raw, _ := json.Marshal(protocol.DefaultNodeAsset())
		var defaults map[string]any
		json.Unmarshal(raw, &defaults)
		for key, value := range defaults {
			if field, ok := props[key].(map[string]any); ok {
				field["default"] = value
			}
		}
		enum("billingCycleUnit", "", "day", "year", "month", "hour", "minute")
		enum("trafficAccounting", "sum", "max", "min", "up", "down")
		enum("currency", protocol.Currencies...)
	case protocol.BatchProbe, protocol.ProbeConfigRequest:
		enum("kind", "http", "https", "tcp", "icmp")
		enum("ipFamily", "", "4", "6")
	}
	return body
}
func typeSchema(t reflect.Type) any {
	if t.Kind() == reflect.Pointer {
		result := typeSchema(t.Elem()).(map[string]any)
		result["nullable"] = true
		return result
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if f.Anonymous && tag[0] == "" {
				// encoding/json promotes untagged embedded struct fields,
				// including the manifest in an update release response.
				embedded := f.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					inline := typeSchema(embedded).(map[string]any)
					for name, field := range inline["properties"].(map[string]any) {
						props[name] = field
					}
					required = append(required, inline["required"].([]string)...)
					continue
				}
			}
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			props[tag[0]] = typeSchema(f.Type)
			if f.Tag.Get("request") == "required" {
				required = append(required, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem()), "nullable": true}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem()), "nullable": true}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Uint64:
		return map[string]any{"type": "integer", "format": "uint64", "minimum": 0, "maximumInteger": "18446744073709551615"}
	case reflect.Uint32:
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 4294967295}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Uint8, reflect.Uint16:
		return map[string]any{"type": "integer", "minimum": 0, "maximum": (1 << t.Bits()) - 1}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return map[string]any{"type": "integer", "minimum": -(1 << (t.Bits() - 1)), "maximum": (1 << (t.Bits() - 1)) - 1}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64", "minimumInteger": "-9223372036854775808", "maximumInteger": "9223372036854775807"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "string"}
	}
}

func updateSchema() any {
	body := schema(update.Request{}).(map[string]any)
	props := body["properties"].(map[string]any)
	props["component"].(map[string]any)["enum"] = []string{"core", "agent"}
	body["required"] = []string{"component", "confirm"}
	return body
}

func renewalSchema() any {
	body := enrollmentSchema().(map[string]any)
	delete(body["properties"].(map[string]any), "displayName")
	return body
}

func enrollmentSchema() any {
	body := schema(agents.EnrollmentRequest{}).(map[string]any)
	properties := body["properties"].(map[string]any)
	properties["deploymentMethod"].(map[string]any)["enum"] = []string{"", agents.DeploymentNative, agents.DeploymentDocker}
	properties["deploymentMethod"].(map[string]any)["default"] = agents.DefaultDeploymentMethod
	properties["asset"] = schema(protocol.NodeAsset{})
	return body
}

// Operations enumerates every administrator API and local lifecycle entry point.
// Request validation, authorization, confirmation and auditing remain in Core/Agent.
func buildOperations() []Operation {
	const node = "/api/v1/nodes/{nodeId}"
	list := []Operation{
		op("updates.check", "GET", "/api/v1/updates/check", "Check stable component release and update preflight", nil, "component", "nodeId", "version"),
		op("updates.status", "GET", "/api/v1/updates/status", "Durable update progress and history", nil, "component", "nodeId", "jobId"),
		op("updates.apply", "POST", "/api/v1/updates/apply", "Confirm and update managed Core or Agent with failure rollback", updateSchema()),
		op("updates.rollback", "POST", "/api/v1/updates/rollback", "Confirm rollback of a successful update", updateSchema()),
		op("core.operations", "GET", "/api/v1/operations", "Shared operation catalog for Web, CLI and TUI", nil),
		op("core.health", "GET", "/api/v1/health", "Core health and build version", nil),
		op("core.protocol", "GET", "/api/v1/protocol", "Wire version, capabilities and transport limits", nil),
		op("auth.csrf", "GET", "/api/v1/auth/csrf", "Issue browser or client CSRF token", nil),
		op("auth.status", "GET", "/api/v1/auth/setup/status", "First-run setup status", nil),
		op("auth.setup", "POST", "/api/v1/auth/setup", "Initialize administrator; read credential and password from a private body file or stdin", schema(protocol.AuthSetupRequest{})),
		op("auth.login", "POST", "/api/v1/auth/login", "Sign in and save a private, server-bound session", schema(protocol.AuthLoginRequest{})),
		op("auth.me", "GET", "/api/v1/auth/me", "Current account", nil),
		op("auth.switch", "POST", "/api/v1/auth/switch", "Switch account after verifying target credentials; revoke the replaced session", schema(protocol.AuthLoginRequest{})),
		op("accounts.list", "GET", "/api/v1/accounts", "Account cards; administrators also see disabled accounts", nil),
		op("accounts.create", "POST", "/api/v1/accounts", "Create administrator or read-only account", schema(protocol.AccountCreateRequest{})),
		op("accounts.update", "PUT", "/api/v1/accounts/{id}", "Edit account or disable it; confirm required; sessions revoked", schema(protocol.AccountUpdateRequest{})),
		op("accounts.delete", "DELETE", "/api/v1/accounts/{id}", "Delete account; confirm required; preserve an enabled administrator", schema(protocol.AccountDeleteRequest{})),
		op("accounts.reset-password", "POST", "/api/v1/accounts/{id}/password", "Reset account password and revoke its sessions; confirm required", schema(protocol.AccountPasswordRequest{})),
		op("profile.get", "GET", "/api/v1/auth/profile", "Current account profile and shared sign-in background", nil),
		op("profile.update", "PUT", "/api/v1/auth/profile", "Update own display name and theme", schema(protocol.ProfileRequest{})),
		op("profile.avatar", "GET", "/api/v1/auth/profile/avatar", "Download own account avatar", nil),
		op("profile.upload-avatar", "POST", "/api/v1/auth/profile/avatar", "Upload own account avatar", nil),
		op("auth.logout", "POST", "/api/v1/auth/logout", "Revoke current session", nil),
		op("auth.password", "POST", "/api/v1/auth/password", "Change own password; invalidates own sessions", schema(protocol.PasswordChangeRequest{})),
		op("sessions.list", "GET", "/api/v1/auth/sessions", "Own account sessions", nil),
		op("sessions.revoke", "DELETE", "/api/v1/auth/sessions/{id}", "Revoke selected session", nil),
		op("nodes.list", "GET", "/api/v1/nodes", "Nodes, online state and Agent capabilities", nil),
		op("nodes.asset", "GET", node+"/asset", "Node cost, expiry and traffic quota settings", nil),
		op("nodes.save-asset", "PUT", node+"/asset", "Save node asset and reminder settings", schema(protocol.NodeAsset{})),
		op("agents.settings", "GET", node+"/agent-settings", "Agent sampling, networking and update settings", nil),
		op("agents.configure", "PUT", node+"/agent-settings", "Persist Agent settings; connection changes apply on reconnect", schema(protocol.AgentSettings{})),
		op("agents.diagnostics", "GET", node+"/diagnostics", "Effective Agent configuration and discovered SSH connection; credentials redacted", nil),
		op("commands.batches", "GET", "/api/v1/commands/batches", "Host command batch history", nil),
		op("commands.batch", "POST", "/api/v1/commands/batches", "Execute a confirmed script on multiple nodes with per-node idempotency", schema(protocol.BatchCommand{})),
		op("commands.batch-status", "GET", "/api/v1/commands/batches/{batchId}", "Batch results; offline nodes are explicitly stale", nil),
		op("commands.batch-cancel", "DELETE", "/api/v1/commands/batches/{batchId}", "Cancel running commands across a batch", nil),
		op("commands.cleanup", "POST", node+"/commands/cleanup", "Clean expired command history and compact completed output; retain retry identities within the retention window", nil),
		op("commands.list", "GET", node+"/commands", "Durable host command history", nil),
		op("commands.submit", "POST", node+"/commands", "Execute a bounded root host script; confirmation and Idempotency-Key required", schema(protocol.CommandSpec{})),
		op("commands.get", "GET", node+"/commands/{jobId}", "Host command status, exit code and bounded output", nil),
		op("commands.cancel", "DELETE", node+"/commands/{jobId}", "Cancel a running host command process group", nil),
		op("nodes.metrics", "GET", node+"/metrics", "Latest host metrics", nil),
		op("nodes.history", "GET", node+"/history", "Metric history; from/to are RFC3339, resolution is 5s (rates, up to 4h), minute or hour", nil, "from", "to", "resolution"),
		op("nodes.preferences", "GET", node+"/preferences", "Node and container display preferences", nil),
		op("nodes.save-preference", "PUT", node+"/preferences", "Save display and connection preference", schema(dashboard.Preference{})),
		op("guest.settings", "GET", "/api/v1/guest/settings", "Guest display policy and permitted metric choices", nil),
		op("guest.configure", "PUT", "/api/v1/guest/settings", "Publish explicitly selected read-only data; empty by default", schema(guest.Settings{})),
		op("guest.preview", "GET", "/api/v1/guest/preview", "Administrator preview of the saved guest projection", nil),
		op("guest.entry", "GET", "/api/v1/public/guest", "Whether guest mode is enabled; no display data", nil),
		op("guest.display", "GET", "/api/v1/public/guest/display", "Only explicitly published guest display data", nil),
		op("dashboard.core-info", "GET", "/api/v1/dashboard/core", "Core runtime host information", nil),
		op("dashboard.memo", "GET", "/api/v1/dashboard/memo", "Read shared overview memo", nil),
		op("dashboard.save-memo", "PUT", "/api/v1/dashboard/memo", "Save shared overview memo with revision check", schema(dashboard.MemoUpdate{})),
		op("dashboard.settings", "GET", "/api/v1/dashboard/settings", "Dashboard settings", nil),
		op("dashboard.save-settings", "PUT", "/api/v1/dashboard/settings", "Save dashboard settings", schema(dashboard.Settings{})),
		op("dashboard.watch", "GET", "/ws/v1/dashboard", "Live dashboard messages as JSONL", nil),
		op("agents.grants", "GET", "/api/v1/agents/enrollment-grants", "List bounded enrollment grants without secrets", nil),
		op("agents.create-grant", "POST", "/api/v1/agents/enrollment-grants", "Create an expiring, revocable shared enrollment key; each Agent gets a separate identity", schema(agents.GrantRequest{})),
		op("agents.revoke-grant", "DELETE", "/api/v1/agents/enrollment-grants/{grantId}", "Revoke a shared enrollment key", nil),
		op("nodes.delete", "DELETE", "/api/v1/nodes/{nodeId}", "Delete a node in any state, its Core data and credentials; does not uninstall Agent or stop remote work; confirm required", fields("confirm:boolean")),
		op("nodes.renew-enrollment", "POST", "/api/v1/nodes/{nodeId}/enrollment", "Replace pending enrollment token and return installation commands; previous command becomes invalid", renewalSchema()),
		op("agents.enrollment", "POST", "/api/v1/agents/enrollments", "Create one-time Agent enrollment with native or Docker installation commands (response contains secret)", enrollmentSchema()),
		op("agents.revoke", "POST", "/api/v1/agents/{agentId}/revoke", "Revoke Agent identity", nil),
		op("agents.rotate", "POST", "/api/v1/agents/{agentId}/rotate", "Rotate Agent credential", nil),
		op("containers.list", "GET", node+"/containers", "Docker inventory", nil),
		op("containers.get", "GET", node+"/containers/{containerId}", "Inspect full container ID", nil),
		op("containers.create", "POST", node+"/containers/create", "Create container task", schema(protocol.ContainerCreateSpec{})),
		op("containers.action", "POST", node+"/containers/{containerId}/actions", "Task: start, stop, restart, pause, resume, rename, delete, rebuild or rebuild_cleanup; deletion requires exact ID confirmation", schema(protocol.ContainerActionRequest{})),
		op("containers.rebuild-plan", "POST", node+"/containers/{containerId}/rebuild/plan", "Preview rebuild and obtain confirmation ID", nil),
		op("containers.logs", "GET", "/ws/v1/streams/logs", "Live container logs as JSONL", nil, "nodeId", "containerId", "tail"),
		op("containers.stats", "GET", "/ws/v1/streams/stats", "Live container stats as JSONL", nil, "nodeId", "containerId"),
		op("images.list", "GET", node+"/images", "Paginated images", nil, "filter", "page", "pageSize"),
		op("images.pull", "POST", node+"/images/pull", "Pull image task; registry credentials stay in private input", schema(protocol.ImagePullRequest{})),
		op("images.delete", "DELETE", node+"/images/{imageId}", "Delete image task; confirmationId must equal full sha256 image ID", schema(protocol.ImageDeleteRequest{})),
		op("compose.list", "GET", node+"/compose/projects", "Compose projects and services", nil),
		op("compose.create", "POST", node+"/compose/projects", "Create and deploy project", schema(protocol.ComposeCreateRequest{})),
		op("compose.config", "GET", node+"/compose/projects/{projectKey}/config/{fileIndex}", "Read Compose YAML and digest", nil),
		op("compose.validate", "POST", node+"/compose/projects/{projectKey}/config/{fileIndex}/validate", "Validate edited Compose YAML", schema(protocol.ComposeValidateRequest{})),
		op("compose.action", "POST", node+"/compose/projects/{projectKey}/tasks", "Task: start, stop, restart, deploy, delete or config_save; delete requires exact deleteConfirmationKey and preserves volumes/config; edits use baseSha256", schema(protocol.ComposeActionRequest{})),
		op("tasks.list", "GET", node+"/tasks", "Task history with pagination", nil, "limit", "after"),
		op("tasks.get", "GET", node+"/tasks/{taskId}", "Task status and result; poll until terminal status", nil),
		op("tasks.audit", "GET", node+"/tasks/{taskId}/audit", "Task audit trail", nil),
		op("tasks.cancel", "DELETE", node+"/tasks/{taskId}", "Cancel undelivered task or running image pull", nil),
		op("files.list", "GET", node+"/files", "Paginated host directory within Agent file policy", nil, "path", "after"),
		op("files.search", "GET", node+"/files/search", "Bounded paginated filename search", nil, "path", "after", "filter", "recursive", "minSize", "maxSize"),
		op("files.preview", "GET", node+"/files/preview", "Authenticated passive media preview; active formats download only", nil, "path"),
		op("files.stat", "GET", node+"/files/stat", "File metadata", nil, "path"),
		op("files.text", "GET", node+"/files/text", "Read text and version for optimistic writes", nil, "path"),
		op("files.download", "GET", node+"/files/download", "Download to --output; never overwrite an existing file", nil, "path"),
		op("files.upload", "PUT", node+"/files/upload", "Upload --input; optional --file-version protects existing content", nil, "path"),
		op("files.action", "POST", node+"/files/tasks", "File tasks including ZIP/TAR/TAR.GZ creation and ZIP/TAR/TAR.GZ/TAR.BZ2 extraction; deletion needs exact confirmationPath", schema(protocol.FileActionRequest{})),
		op("terminal.open", "POST", node+"/terminals", "Open host/container shell; TUI is interactive, CLI uses JSONL frames with automatic streamId", schema(protocol.TerminalCreateRequest{})),
		op("terminal.create", "POST", node+"/terminals", "Authorize host/container terminal; returns a single-use ticket", schema(protocol.TerminalCreateRequest{})),
		op("terminal.resume", "POST", node+"/terminals/{streamId}/resume", "Reattach the same tmux session after transport loss", fields("")),
		op("terminal.cancel", "DELETE", node+"/terminals/{streamId}", "End terminal session or cancel pending authorization", nil),
		op("terminal.connect", "GET", "/ws/v1/streams/terminal", "Bidirectional terminal JSONL frames; ticket from terminal.create, provide it via --query @file", nil, "ticket"),
		op("probes.batch", "POST", "/api/v1/probes/batch", "Create the same probe on multiple nodes", schema(protocol.BatchProbe{})),
		op("probes.list", "GET", "/api/v1/probes", "Service probes", nil),
		op("probes.get", "GET", "/api/v1/probes/{id}", "Probe configuration", nil),
		op("probes.create", "POST", "/api/v1/probes", "Create HTTP, HTTPS, TCP or ICMP probe", schema(protocol.ProbeConfigRequest{})),
		op("probes.update", "PUT", "/api/v1/probes/{id}", "Update probe using current revision", nil),
		op("probes.delete", "DELETE", "/api/v1/probes/{id}", "Delete probe using current revision", nil, "revision"),
		op("probes.quality", "GET", "/api/v1/probes/{id}/quality", "Latency percentiles and failure rates; offline gaps are unknown", nil, "from", "to"),
		op("nodes.recording", "GET", node+"/recording", "Bounded raw diagnostic replay and incident/change timeline; rolling two hours, incident windows seven days", nil, "from", "to"),
		op("nodes.incidents", "GET", node+"/incidents", "Recent diagnostic incidents; seven-day retention, maximum 100", nil),
		op("nodes.analysis", "GET", node+"/analysis", "24-hour baseline, trend, capacity forecast and optional 30-minute before/after comparison; unknown when evidence is insufficient", nil, "key", "capacity", "changeAt"),
		op("nodes.correlations", "GET", node+"/correlations", "Aligned minute correlation against up to eight node IDs; correlation does not establish cause", nil, "key", "nodes"),
		op("metrics.catalog", "GET", "/api/v1/metrics/catalog", "Host metrics available for history and threshold alerts", nil),
		op("probes.history", "GET", "/api/v1/probes/{id}/history", "Probe runs", nil, "limit"),
		op("alerts.list", "GET", "/api/v1/alerts", "Active alerts", nil, "limit"),
		op("alerts.history", "GET", "/api/v1/alerts/history", "Alert history", nil, "limit"),
		op("alerts.events", "GET", "/api/v1/alerts/events", "Alert events", nil, "alertId", "limit"),
		op("alerts.acknowledge", "POST", "/api/v1/alerts/{id}/acknowledge", "Acknowledge alert", fields("note:string")),
		op("alerts.silence", "POST", "/api/v1/alerts/{id}/silence", "Silence until RFC3339 time", fields("until:string reason:string")),
		op("rules.list", "GET", "/api/v1/alerts/rules", "Alert rules", nil, "nodeId"),
		op("rules.create", "POST", "/api/v1/alerts/rules", "Create alert rule", schema(alerts.RuleInput{})),
		op("rules.update", "PUT", "/api/v1/alerts/rules/{id}", "Update rule with current revision", schema(alerts.RuleInput{})),
		op("rules.delete", "DELETE", "/api/v1/alerts/rules/{id}", "Delete rule with current revision", fields("revision:integer")),
		op("rules.defaults", "POST", "/api/v1/alerts/rules/defaults", "Create default rules for node", fields("nodeId:string")),
		op("channels.list", "GET", "/api/v1/alerts/channels", "Notification channels; secrets are masked", nil),
		op("channels.create", "POST", "/api/v1/alerts/channels", "Create webhook or TLS SMTP channel", schema(alerts.ChannelInput{})),
		op("channels.update", "PUT", "/api/v1/alerts/channels/{id}", "Update notification channel with revision", schema(alerts.ChannelInput{})),
		op("channels.delete", "DELETE", "/api/v1/alerts/channels/{id}", "Delete channel with revision", fields("revision:integer")),
		op("channels.test", "POST", "/api/v1/alerts/channels/{id}/test", "Send test notification", nil),
		op("windows.list", "GET", "/api/v1/alerts/windows", "Silence and maintenance windows", nil),
		op("windows.create", "POST", "/api/v1/alerts/windows", "Create silence or maintenance window", schema(alerts.WindowInput{})),
		op("windows.delete", "DELETE", "/api/v1/alerts/windows/{id}", "Delete window", nil),
		op("feeds.options", "GET", "/api/v1/feeds/options", "Subscription refresh intervals and limits", nil),
		op("feeds.list", "GET", "/api/v1/feeds", "RSS and Atom subscriptions", nil),
		op("feeds.create", "POST", "/api/v1/feeds", "Create a Core-local subscription", schema(feeds.Input{})),
		op("feeds.get", "GET", "/api/v1/feeds/{id}", "Subscription refresh status", nil),
		op("feeds.update", "PUT", "/api/v1/feeds/{id}", "Update subscription using current revision", schema(feeds.Input{})),
		op("feeds.delete", "DELETE", "/api/v1/feeds/{id}", "Delete subscription and its entries; confirm required", fields("revision:integer confirm:boolean")),
		op("feeds.refresh", "POST", "/api/v1/feeds/{id}/refresh", "Queue refresh; coalesce pending requests", nil),
		op("feeds.cancel", "POST", "/api/v1/feeds/{id}/cancel", "Cancel queued or running refresh", nil),
		op("feeds.items", "GET", "/api/v1/feeds/{id}/items", "Subscription entries with pagination and search", nil, "offset", "limit", "unread", "search"),
		op("feeds.read", "PUT", "/api/v1/feeds/{id}/items/{itemId}", "Set shared read state", fields("read:boolean")),
		op("feeds.deliveries", "GET", "/api/v1/feeds/{id}/deliveries", "Subscription notification history", nil),
		op("feeds.retry-delivery", "POST", "/api/v1/feeds/{id}/deliveries/{deliveryId}/retry", "Retry failed subscription notification", nil),
		op("deliveries.list", "GET", "/api/v1/alerts/deliveries", "Notification delivery history", nil, "limit"),
		op("discovery.peers", "GET", "/api/v1/discovery/tailscale", "Detect existing Tailscale Linux peers; never configure Tailscale", nil),
		op("discovery.host-key", "POST", "/api/v1/discovery/tailscale/host-key", "Read SSH host fingerprint for independent verification", schema(protocol.HostKeyRequest{})),
		op("deployments.list", "GET", "/api/v1/discovery/deployments", "Agent deployment tasks", nil),
		op("deployments.get", "GET", "/api/v1/discovery/deployments/{taskId}", "Agent deployment task", nil),
		op("deployments.create", "POST", "/api/v1/discovery/deployments", "Deploy root Agent to verified peer", schema(coretailscale.DeployRequest{})),
		op("deployments.resolve", "POST", "/api/v1/discovery/deployments/{taskId}/resolve", "Record manual recovery outcome", schema(protocol.ResolveDeploymentRequest{})),
		op("appearance.get", "GET", "/api/v1/public/appearance", "Appearance settings", nil),
		op("appearance.update", "PUT", "/api/v1/settings/appearance", "Update shared sign-in appearance and own account profile", schema(protocol.AppearanceRequest{})),
	}
	for _, kind := range []string{"avatar", "background"} {
		list = append(list, op("appearance."+kind, "GET", "/api/v1/public/appearance/"+kind, "Download appearance image to --output", nil), op("appearance.upload-"+kind, "POST", "/api/v1/settings/appearance/"+kind, "Upload raster image from --input", nil))
	}
	for i := range list {
		o := &list[i]
		if o.Name == "nodes.recording" {
			o.MaxResponseBytes = 32 << 20
		}
		o.RequiredRole = "administrator"
		switch o.Name {
		case "core.operations", "core.health", "core.protocol", "auth.csrf", "auth.status", "auth.setup", "auth.login", "guest.entry", "guest.display", "appearance.get", "appearance.avatar", "appearance.background":
			o.RequiredRole = "public"
		case "auth.me", "auth.logout", "auth.password", "auth.switch", "sessions.list", "sessions.revoke", "accounts.list",
			"profile.get", "profile.update", "profile.avatar", "profile.upload-avatar",
			"nodes.list", "nodes.metrics", "nodes.history", "nodes.preferences", "nodes.asset",
			"dashboard.core-info", "dashboard.memo", "dashboard.settings", "dashboard.watch", "metrics.catalog", "nodes.recording", "nodes.incidents", "nodes.analysis", "nodes.correlations",
			"containers.list", "images.list", "compose.list", "probes.list", "probes.history", "probes.quality":
			o.RequiredRole = "viewer"
		}
		switch o.Name {
		case "feeds.refresh":
			o.Completion = &CompletionContract{Operation: "feeds.get", Params: map[string]string{"id": "params.id"}, StatusPath: "status", TerminalStatuses: []string{"succeeded", "failed", "canceled", "inactive"}, SuccessStatuses: []string{"succeeded"}}
		case "probes.batch", "commands.batch", "commands.submit", "updates.apply", "updates.rollback", "containers.create", "containers.action", "images.pull", "images.delete", "compose.create", "compose.action", "files.action", "files.upload", "deployments.create":
			o.Idempotent = true
		}
		if o.Name == "terminal.open" {
			o.Transport = "terminal"
		}
		if strings.HasPrefix(o.Path, "/ws/") {
			o.Transport = "websocket"
		}
		if o.Name == "files.download" || o.Name == "files.preview" || o.Name == "appearance.avatar" || o.Name == "appearance.background" || o.Name == "profile.avatar" {
			o.Transport = "download"
		}
		if o.Name == "files.upload" {
			o.Transport = "upload"
		}
		if strings.HasPrefix(o.Name, "appearance.upload-") || o.Name == "profile.upload-avatar" {
			o.Transport = "multipart"
		}
	}
	for i := range list {
		if list[i].Name == "probes.update" {
			for _, o := range list {
				if o.Name == "probes.create" {
					list[i].BodySchema = o.BodySchema
				}
			}
		}
	}
	for i := range list {
		if list[i].Name == "containers.rebuild-plan" {
			list[i].BodySchema = schema(protocol.RebuildPlanRequest{})
		}
	}
	for _, operation := range list {
		markSecrets(operation.BodySchema)
	}
	for i := range list {
		describeTransport(&list[i])
	}
	return list
}

func markSecrets(value any) {
	body, ok := value.(map[string]any)
	if !ok {
		return
	}
	markSecrets(body["items"])
	properties, _ := body["properties"].(map[string]any)
	for name, value := range properties {
		field, ok := value.(map[string]any)
		if !ok {
			continue
		}
		lower := strings.ToLower(name)
		if lower == "secret" || strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "privatekey") || strings.Contains(lower, "passphrase") {
			field["writeOnly"] = true
		}
		markSecrets(field)
	}
}

var operationCatalog = sync.OnceValue(func() []Operation { return buildOperations() })
var operationIndex = sync.OnceValue(func() map[string]Operation {
	index := map[string]Operation{}
	for _, o := range operationCatalog() {
		index[o.Name] = o
	}
	return index
})

// Operations returns caller-owned schemas; translations or external callers
// cannot mutate the shared catalog used by concurrent CLI/TUI requests.
func Operations() []Operation {
	source := operationCatalog()
	result := make([]Operation, len(source))
	for i, o := range source {
		result[i] = cloneOperation(o)
	}
	return result
}
func cloneSchema(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = cloneSchema(item)
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(v))
		for key, item := range v {
			result[key] = item
		}
		return result
	case []string:
		return append([]string{}, v...)
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = cloneSchema(item)
		}
		return result
	default:
		return value
	}
}
func cloneOperation(o Operation) Operation {
	o.Parameters = append([]string{}, o.Parameters...)
	o.Query = append([]string{}, o.Query...)
	o.BodySchema = cloneSchema(o.BodySchema)
	o.ResponseSchema = cloneSchema(o.ResponseSchema)
	if o.Headers != nil {
		o.Headers = cloneSchema(o.Headers).(map[string]any)
	}
	if o.Upload != nil {
		copy := *o.Upload
		o.Upload = &copy
	}
	if o.Completion != nil {
		copy := *o.Completion
		copy.Params = cloneSchema(copy.Params).(map[string]string)
		if copy.Query != nil {
			copy.Query = cloneSchema(copy.Query).(map[string]string)
		}
		copy.TerminalStatuses = append([]string{}, copy.TerminalStatuses...)
		copy.SuccessStatuses = append([]string{}, copy.SuccessStatuses...)
		o.Completion = &copy
	}
	return o
}
func Find(name string) (Operation, error) {
	if o, ok := operationIndex()[name]; ok {
		return cloneOperation(o), nil
	}
	return Operation{}, fmt.Errorf("unknown operation %q; run nodedance operations", name)
}
func (o Operation) URLPath(params map[string]string) (string, error) {
	path := o.Path
	for _, key := range o.Parameters {
		v := params[key]
		if v == "" || v == "." || v == ".." || strings.ContainsAny(v, "/\\\x00\r\n") {
			return "", fmt.Errorf("invalid or missing parameter %s", key)
		}
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(v))
	}
	for key := range params {
		found := false
		for _, p := range o.Parameters {
			if p == key {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("unknown path parameter %s", key)
		}
	}
	return path, nil
}
