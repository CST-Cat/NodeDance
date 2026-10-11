// Package control is the operation contract and remote client shared by CLI and TUI.
// It uses the Core's public API; it never opens its database or contacts Agents directly.
package control

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type Operation struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Parameters  []string `json:"parameters"`
	Query       []string `json:"query"`
	BodySchema  any      `json:"bodySchema,omitempty"`
	Transport   string   `json:"transport"` // json, download, upload, multipart, websocket, local
	Idempotent  bool     `json:"requiresIdempotencyKey,omitempty"`
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
func schema(value any) any { return typeSchema(reflect.TypeOf(value)) }
func typeSchema(t reflect.Type) any {
	if t.Kind() == reflect.Pointer {
		return typeSchema(t.Elem())
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
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			props[tag[0]] = typeSchema(f.Type)
			if !strings.Contains(f.Tag.Get("json"), "omitempty") {
				required = append(required, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Uint32:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "string"}
	}
}

// Operations enumerates every administrator API and local lifecycle entry point.
// Request validation, authorization, confirmation and auditing remain in Core/Agent.
func buildOperations() []Operation {
	const node = "/api/v1/nodes/{nodeId}"
	list := []Operation{
		op("core.health", "GET", "/api/v1/health", "Core health and build version", nil),
		op("core.protocol", "GET", "/api/v1/protocol", "Wire version, capabilities and transport limits", nil),
		op("auth.status", "GET", "/api/v1/auth/setup/status", "First-run setup status", nil),
		op("auth.setup", "POST", "/api/v1/auth/setup", "Initialize administrator; read credential and password from a private body file or stdin", fields("credential:string password:string displayName:string")),
		op("auth.login", "POST", "/api/v1/auth/login", "Sign in and save a private, server-bound session", fields("password:string")),
		op("auth.me", "GET", "/api/v1/auth/me", "Current administrator", nil),
		op("auth.logout", "POST", "/api/v1/auth/logout", "Revoke current session", nil),
		op("auth.password", "POST", "/api/v1/auth/password", "Change administrator password; invalidates sessions", fields("currentPassword:string newPassword:string")),
		op("sessions.list", "GET", "/api/v1/auth/sessions", "Administrator sessions", nil),
		op("sessions.revoke", "DELETE", "/api/v1/auth/sessions/{id}", "Revoke selected session", nil),
		op("nodes.list", "GET", "/api/v1/nodes", "Nodes, online state and Agent capabilities", nil),
		op("nodes.metrics", "GET", node+"/metrics", "Latest host metrics", nil),
		op("nodes.history", "GET", node+"/history", "Metric history; from/to are RFC3339, resolution is minute or hour", nil, "from", "to", "resolution"),
		op("nodes.preferences", "GET", node+"/preferences", "Node and container display preferences", nil),
		op("nodes.save-preference", "PUT", node+"/preferences", "Save display and connection preference", schema(dashboard.Preference{})),
		op("dashboard.settings", "GET", "/api/v1/dashboard/settings", "Dashboard settings", nil),
		op("dashboard.save-settings", "PUT", "/api/v1/dashboard/settings", "Save dashboard settings", schema(dashboard.Settings{})),
		op("dashboard.watch", "GET", "/ws/v1/dashboard", "Live dashboard messages as JSONL", nil),
		op("agents.enrollment", "POST", "/api/v1/agents/enrollments", "Create short-lived one-time Agent enrollment (response contains secret)", fields("displayName:string coreUrl?:string")),
		op("agents.revoke", "POST", "/api/v1/agents/{agentId}/revoke", "Revoke Agent identity", nil),
		op("agents.rotate", "POST", "/api/v1/agents/{agentId}/rotate", "Rotate Agent credential", nil),
		op("containers.list", "GET", node+"/containers", "Docker inventory", nil),
		op("containers.get", "GET", node+"/containers/{containerId}", "Inspect full container ID", nil),
		op("containers.create", "POST", node+"/containers/create", "Create container task", schema(protocol.ContainerCreateSpec{})),
		op("containers.action", "POST", node+"/containers/{containerId}/actions", "Task: start, stop, restart, pause, resume, rename, delete, rebuild or rebuild_cleanup; deletion requires exact ID confirmation", fields("action:string newName?:string deleteConfirmed?:boolean deleteConfirmationId?:string confirmationId?:string")),
		op("containers.rebuild-plan", "POST", node+"/containers/{containerId}/rebuild/plan", "Preview rebuild and obtain confirmation ID", nil),
		op("containers.logs", "GET", "/ws/v1/streams/logs", "Live container logs as JSONL", nil, "nodeId", "containerId", "tail"),
		op("containers.stats", "GET", "/ws/v1/streams/stats", "Live container stats as JSONL", nil, "nodeId", "containerId"),
		op("images.list", "GET", node+"/images", "Paginated images", nil, "filter", "page", "pageSize"),
		op("images.pull", "POST", node+"/images/pull", "Pull image task; registry credentials stay in private input", fields("imageReference:string username?:string password?:string")),
		op("images.delete", "DELETE", node+"/images/{imageId}", "Delete image task; confirmationId must equal full sha256 image ID", fields("deleteConfirmed:boolean confirmationId:string")),
		op("compose.list", "GET", node+"/compose/projects", "Compose projects and services", nil),
		op("compose.create", "POST", node+"/compose/projects", "Create and deploy project", fields("name:string workingDirectory:string content:string")),
		op("compose.config", "GET", node+"/compose/projects/{projectKey}/config/{fileIndex}", "Read Compose YAML and digest", nil),
		op("compose.validate", "POST", node+"/compose/projects/{projectKey}/config/{fileIndex}/validate", "Validate edited Compose YAML", fields("content:string")),
		op("compose.action", "POST", node+"/compose/projects/{projectKey}/tasks", "Task: start, stop, restart, deploy or config_save; edits use baseSha256", fields("action:string fileIndex?:integer content?:string baseSha256?:string")),
		op("tasks.list", "GET", node+"/tasks", "Task history with pagination", nil, "limit", "after"),
		op("tasks.get", "GET", node+"/tasks/{taskId}", "Task status and result; poll until terminal status", nil),
		op("tasks.audit", "GET", node+"/tasks/{taskId}/audit", "Task audit trail", nil),
		op("tasks.cancel", "DELETE", node+"/tasks/{taskId}", "Cancel undelivered task or running image pull", nil),
		op("files.list", "GET", node+"/files", "List host directory within Agent file policy", nil, "path"),
		op("files.stat", "GET", node+"/files/stat", "File metadata", nil, "path"),
		op("files.text", "GET", node+"/files/text", "Read text and version for optimistic writes", nil, "path"),
		op("files.download", "GET", node+"/files/download", "Download to --output; never overwrite an existing file", nil, "path"),
		op("files.upload", "PUT", node+"/files/upload", "Upload --input; optional --file-version protects existing content", nil, "path"),
		op("files.action", "POST", node+"/files/tasks", "Task: mkdir, rename, delete or save_text; deletion needs exact confirmationPath", fields("action:string path:string newPath?:string expectedVersion?:string deleteConfirmed?:boolean confirmationPath?:string content?:string")),
		op("terminal.open", "POST", node+"/terminals", "Open host/container shell; TUI is interactive, CLI uses JSONL frames with automatic streamId", fields("targetKind:string containerId?:string")),
		op("terminal.create", "POST", node+"/terminals", "Authorize host/container terminal; returns a single-use ticket", fields("targetKind:string containerId?:string")),
		op("terminal.cancel", "DELETE", node+"/terminals/{streamId}", "Cancel pending terminal authorization", nil),
		op("terminal.connect", "GET", "/ws/v1/streams/terminal", "Bidirectional terminal JSONL frames; ticket from terminal.create, provide it via --query @file", nil, "ticket"),
		op("probes.list", "GET", "/api/v1/probes", "Service probes", nil),
		op("probes.get", "GET", "/api/v1/probes/{id}", "Probe configuration", nil),
		op("probes.create", "POST", "/api/v1/probes", "Create HTTP, HTTPS or TCP probe", fields("nodeId:string name:string kind:string target:string expectedHttpStatus?:integer intervalSeconds:integer timeoutSeconds:integer enabled?:boolean revision?:integer")),
		op("probes.update", "PUT", "/api/v1/probes/{id}", "Update probe using current revision", nil),
		op("probes.delete", "DELETE", "/api/v1/probes/{id}", "Delete probe using current revision", nil, "revision"),
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
		op("deliveries.list", "GET", "/api/v1/alerts/deliveries", "Notification delivery history", nil, "limit"),
		op("discovery.peers", "GET", "/api/v1/discovery/tailscale", "Detect existing Tailscale Linux peers; never configure Tailscale", nil),
		op("discovery.host-key", "POST", "/api/v1/discovery/tailscale/host-key", "Read SSH host fingerprint for independent verification", fields("peerIdentity:string")),
		op("deployments.list", "GET", "/api/v1/discovery/deployments", "Agent deployment tasks", nil),
		op("deployments.get", "GET", "/api/v1/discovery/deployments/{taskId}", "Agent deployment task", nil),
		op("deployments.create", "POST", "/api/v1/discovery/deployments", "Deploy root Agent to verified peer", nil),
		op("deployments.resolve", "POST", "/api/v1/discovery/deployments/{taskId}/resolve", "Record manual recovery outcome", fields("outcome:string observedState:string confirmed:boolean")),
		op("appearance.get", "GET", "/api/v1/public/appearance", "Appearance settings", nil),
		op("appearance.update", "PUT", "/api/v1/settings/appearance", "Update display name and appearance", fields("displayName:string theme:string backgroundColor:string")),
	}
	for _, kind := range []string{"avatar", "background"} {
		list = append(list, op("appearance."+kind, "GET", "/api/v1/public/appearance/"+kind, "Download appearance image to --output", nil), op("appearance.upload-"+kind, "POST", "/api/v1/settings/appearance/"+kind, "Upload raster image from --input", nil))
	}
	for i := range list {
		o := &list[i]
		switch o.Name {
		case "containers.create", "containers.action", "images.pull", "images.delete", "compose.create", "compose.action", "files.action", "files.upload", "deployments.create":
			o.Idempotent = true
		}
		if o.Name == "terminal.open" {
			o.Transport = "terminal"
		}
		if strings.HasPrefix(o.Path, "/ws/") {
			o.Transport = "websocket"
		}
		if o.Name == "files.download" || o.Name == "appearance.avatar" || o.Name == "appearance.background" {
			o.Transport = "download"
		}
		if o.Name == "files.upload" {
			o.Transport = "upload"
		}
		if strings.HasPrefix(o.Name, "appearance.upload-") {
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
		if list[i].Name == "containers.action" {
			list[i].BodySchema.(map[string]any)["properties"].(map[string]any)["rebuild"] = schema(protocol.RebuildSpec{})
		}
		if list[i].Name == "containers.rebuild-plan" {
			list[i].BodySchema = map[string]any{"type": "object", "required": []string{"spec"}, "additionalProperties": false, "properties": map[string]any{"spec": schema(protocol.RebuildSpec{})}}
		}
		if list[i].Name == "deployments.create" {
			b := fields("peerIdentity:string displayName:string fileRoot?:string disableFileRoot?:boolean coreUrl:string fallbackUrl?:string allowFallback:boolean hostFingerprint:string confirmHostKey:boolean confirmChangedHostKey:boolean").(map[string]any)
			b["properties"].(map[string]any)["credentials"] = fields("user:string password?:string privateKey?:string passphrase?:string")
			b["required"] = append(b["required"].([]string), "credentials")
			list[i].BodySchema = b
		}
	}
	return list
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
		if v == "" || v == "." || v == ".." || strings.ContainsAny(v, "/\\\x00") {
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
