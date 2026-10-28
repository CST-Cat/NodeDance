package control

import (
	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/CST-Cat/NodeDance/internal/update"
	"strings"
	"sync"

	"github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/guest"
	"github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// Request tags describe input presence; encoding/json omission describes output
// encoding and must not be used as request validation.
func describeTransport(o *Operation) {
	if o.Transport == "json" || o.Transport == "terminal" {
		o.ResponseSchema = map[string]any{"type": "object"}
	}
	if o.Name == "dashboard.save-memo" {
		p := o.BodySchema.(map[string]any)["properties"].(map[string]any)
		p["content"].(map[string]any)["maxBytes"] = dashboard.MaxMemoBytes
		p["revision"].(map[string]any)["minimum"] = 1
	}
	if o.BodySchema != nil {
		o.MaxBodyBytes = 16 * 1024
	}
	if o.Name == "dashboard.save-memo" {
		o.MaxBodyBytes = 6*dashboard.MaxMemoBytes + 1024
	}
	if o.Idempotent {
		o.Headers = map[string]any{"Idempotency-Key": map[string]any{"type": "string", "minLength": 1, "maxBytes": taskstate.MaxIdempotencyKeyBytes, "description": "Reuse for retries of the same intent"}}
	}
	if o.Transport == "multipart" {
		o.Upload = &UploadContract{Field: protocol.AppearanceImageField, ContentType: "multipart/form-data", MaxBytes: protocol.MaxAppearanceImageBytes}
	}
	if o.Transport == "terminal" || o.Name == "terminal.connect" {
		o.Duplex = true
	}
	if o.Transport == "upload" {
		if o.Headers == nil {
			o.Headers = map[string]any{}
		}
		o.Upload = &UploadContract{ContentType: "application/octet-stream", MaxBytes: protocol.DefaultFileLimit}
		o.Headers["X-File-Version"] = map[string]any{"type": "string", "description": "Expected existing file version; omit only for an unconditional write"}
	}
	if o.Idempotent && (strings.HasPrefix(o.Name, "commands.") || strings.HasPrefix(o.Name, "updates.") || o.Name == "probes.batch" || o.Name == "deployments.create") {
		h := o.Headers["Idempotency-Key"].(map[string]any)
		delete(h, "minLength")
		h["minBytes"] = 8
		h["maxBytes"] = 128
	}
	if o.Name == "shares.upload" {
		o.Headers["X-Share-Password"] = map[string]any{"type": "string", "maxBytes": 72, "writeOnly": true}
		delete(o.Headers, "X-File-Version")
	}
	if o.Name == "share-requests.upload" {
		delete(o.Headers, "X-File-Version")
	}
	if o.Name == "files.text" {
		o.MaxResponseBytes = 6*protocol.MaxTextFileBytes + 8192
		o.ResponseSchema = map[string]any{"type": "object", "required": []string{"text", "version"}, "properties": map[string]any{"text": map[string]any{"type": "string", "maxBytes": protocol.MaxTextFileBytes}, "version": map[string]any{"type": "string"}}, "additionalProperties": true}
	}
	props := func() map[string]any {
		if b, ok := o.BodySchema.(map[string]any); ok {
			if p, ok := b["properties"].(map[string]any); ok {
				return p
			}
		}
		return nil
	}
	require := func(keys ...string) { o.BodySchema.(map[string]any)["required"] = keys }
	field := func(key string, values map[string]any) {
		p, ok := props()[key].(map[string]any)
		if !ok {
			return
		}
		for k, v := range values {
			p[k] = v
		}
	}
	switch o.Name {
	case "guest.configure":
		o.MaxBodyBytes = 64 * 1024
	case "agents.configure":
		o.MaxBodyBytes = 128 << 10
		field("commandHistoryRetentionDays", map[string]any{"minimum": 1, "maximum": 3650, "default": 90})
	case "appearance.update":
		o.MaxBodyBytes = 1 << 20
		field("sharks", map[string]any{"items": map[string]any{
			"type": "object",
			"required": []string{"kind", "size"},
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "enum": []string{"classic", "hammerhead", "zebra", "tiger", "greathammer", "mako", "thresher", "nurse", "turtle", "manta", "dolphin", "whale", "tang"}},
				"size": map[string]any{"type": "number", "exclusiveMinimum": 0},
			},
		}})
	case "commands.submit", "commands.batch":
		o.MaxBodyBytes = protocol.MaxCommandRequestBytes
		require("script", "timeoutSeconds", "confirm")
		if o.Name == "commands.batch" {
			require("nodeIds", "script", "timeoutSeconds", "confirm")
		}
		field("script", map[string]any{"minLength": 1, "maxBytes": protocol.MaxCommandScriptBytes})
		field("timeoutSeconds", map[string]any{"minimum": protocol.MinCommandTimeoutSeconds, "maximum": protocol.MaxCommandTimeoutSeconds})
		field("confirm", map[string]any{"enum": []any{true}})
	case "nodes.delete":
		require("confirm")
		field("confirm", map[string]any{"enum": []any{true}})
	case "agents.enrollment":
		require("displayName")
	case "containers.create":
		o.MaxBodyBytes = protocol.MaxContainerCreateBytes
		require("image", "name", "restartPolicy")
	case "deployments.create":
		o.MaxBodyBytes = 128 << 10
	case "probes.create", "probes.update", "probes.batch":
		field("kind", map[string]any{"enum": []string{protocol.ProbeHTTP, protocol.ProbeHTTPS, protocol.ProbeTCP, protocol.ProbeICMP}})
		field("intervalSeconds", map[string]any{"minimum": protocol.MinProbeIntervalSeconds, "maximum": protocol.MaxProbeIntervalSeconds})
		field("timeoutSeconds", map[string]any{"minimum": protocol.MinProbeTimeoutSeconds, "maximum": protocol.MaxProbeTimeoutSeconds})
		field("target", map[string]any{"maxBytes": protocol.MaxProbeTargetBytes})
	case "nodes.save-preference":
		require("targetKind", "identity")
	case "agents.create-grant":
		require("displayPrefix", "maxUses", "lifetimeSeconds")
	case "nodes.save-asset":
		require("currency", "billingCycleDays", "trafficAccounting")
	case "rules.create", "rules.update":
		require("name", "kind", "nodeId", "severity")
	case "channels.create", "channels.update":
		require("name", "kind", "config")
		field("secret", map[string]any{"writeOnly": true, "maxLength": 4096})
		field("signingSecret", map[string]any{"writeOnly": true, "maxLength": 4096})
		field("headers", map[string]any{"writeOnly": true, "maxProperties": 20})
		field("kind", map[string]any{"enum": []string{alerts.ChannelWebhook, alerts.ChannelSMTP}})
		if config, ok := props()["config"].(map[string]any); ok {
			if fields, ok := config["properties"].(map[string]any); ok {
				providers := []string{}
				for _, p := range alerts.ChannelProviders() {
					providers = append(providers, p.ID)
				}
				fields["provider"].(map[string]any)["enum"] = providers
				fields["method"].(map[string]any)["enum"] = []string{"POST", "PUT", "PATCH"}
				fields["bodyTemplate"].(map[string]any)["maxBytes"] = 16384
			}
		}
	case "windows.create":
		require("kind", "scopeType", "startsAt", "endsAt")
	case "files.action":
		field("action", map[string]any{"enum": []string{"mkdir", "rename", "move", "copy", "archive", "extract", "delete", "save_text"}})
		field("format", map[string]any{"enum": []string{"zip", "tar", "tar.gz"}, "description": "Archive format; inferred from .zip/.tar/.tar.gz/.tgz filename when omitted"})
		field("paths", map[string]any{"maxItems": 100, "items": map[string]any{"type": "string", "maxBytes": 4096}})
		o.MaxBodyBytes = 6*protocol.MaxTextFileBytes + 8*1024
		field("content", map[string]any{"maxBytes": protocol.MaxTextFileBytes})
	case "compose.create", "compose.validate", "compose.action":
		o.MaxBodyBytes = 6*protocol.MaxComposeFileBytes + 16*1024
		field("content", map[string]any{"maxBytes": protocol.MaxComposeFileBytes})
	case "updates.apply", "updates.rollback", "updates.delete-backup":
		o.MaxBodyBytes = 65536
		if o.Name == "updates.delete-backup" {
			require("component", "jobId", "confirm")
		}
	case "metrics.catalog":
		o.ResponseSchema = map[string]any{"type": "object", "required": []string{"metrics"}, "properties": map[string]any{"metrics": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}}}
	}
	completion := func(name, id, reference, status string) {
		o.Completion = &CompletionContract{Operation: name, Params: map[string]string{id: reference}, StatusPath: status, TerminalStatuses: []string{"succeeded", "failed", "canceled", "timed_out", "unknown"}, SuccessStatuses: []string{"succeeded"}}
	}
	switch o.Name {
	case "containers.create", "containers.action", "images.pull", "images.delete", "compose.create", "compose.action", "files.action", "files.upload":
		completion("tasks.get", "taskId", "data.taskId", "status")
		o.Completion.Params["nodeId"] = "params.nodeId"
	case "commands.submit":
		completion("commands.get", "jobId", "data.job.id", "job.status")
		o.Completion.Params["nodeId"] = "params.nodeId"
		o.Completion.TerminalStatuses = []string{"succeeded", "failed", "cancelled", "timed_out", "interrupted", "unknown"}
	case "deployments.create":
		completion("deployments.get", "taskId", "data.taskId", "status")
	case "updates.apply", "updates.rollback":
		completion("updates.status", "", "", "status")
		o.Completion.Params = map[string]string{}
		o.Completion.Query = map[string]string{"component": "body.component", "nodeId": "body.nodeId", "jobId": "data.id"}
		o.Completion.TerminalStatuses = []string{"succeeded", "failed", "rolled_back", "rollback_failed", "journal_failed"}
	}
	describeResponse(o)
}

type compiledRoute struct {
	operation Operation
	parts     []string
}

var routeIndex = sync.OnceValue(func() map[string]map[int][]compiledRoute {
	index := make(map[string]map[int][]compiledRoute)
	for _, operation := range operationCatalog() {
		byLength := index[operation.Method]
		if byLength == nil {
			byLength = make(map[int][]compiledRoute)
			index[operation.Method] = byLength
		}
		parts := strings.Split(operation.Path, "/")
		byLength[len(parts)] = append(byLength[len(parts)], compiledRoute{operation: operation, parts: parts})
	}
	return index
})

type RequestContract struct {
	Name         string
	RequiredRole string
	Transport    string
	BodySchema   any
	Headers      map[string]any
	MaxBodyBytes int64
}

func MatchRoute(method, path string) (RequestContract, bool) {
	parts := strings.Split(path, "/")
	for _, route := range routeIndex()[method][len(parts)] {
		match := true
		for i, part := range route.parts {
			if strings.HasPrefix(part, "{") {
				if parts[i] == "" {
					match = false
					break
				}
			} else if part != parts[i] {
				match = false
				break
			}
		}
		if match {
			operation := route.operation
			request := RequestContract{Name: operation.Name, RequiredRole: operation.RequiredRole, Transport: operation.Transport, BodySchema: cloneSchema(operation.BodySchema), MaxBodyBytes: operation.MaxBodyBytes}
			if operation.Headers != nil {
				request.Headers = cloneSchema(operation.Headers).(map[string]any)
			}
			return request, true
		}
	}
	return RequestContract{}, false
}

func responseObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties}
}
func describeResponse(o *Operation) {
	text := map[string]any{"type": "string"}
	job := schema(protocol.CommandJob{}).(map[string]any)
	job["required"] = []string{"id", "status"}
	task := responseObject([]string{"taskId", "status"}, map[string]any{"taskId": text, "status": text, "nodeId": text, "progress": responseObject(nil, map[string]any{"completed": map[string]any{"type": "integer"}, "total": map[string]any{"type": "integer"}})})
	list := func(key string, item any) any {
		return responseObject([]string{key}, map[string]any{key: map[string]any{"type": "array", "items": item}})
	}
	wrapped := func(key string, item any) any { return responseObject([]string{key}, map[string]any{key: item}) }
	switch o.Name {
	case "guest.settings":
		o.ResponseSchema = responseObject([]string{"settings", "metrics", "historyHours"}, map[string]any{"settings": schema(guest.Settings{}), "metrics": map[string]any{"type": "array", "items": schema(guest.Metric{})}, "historyHours": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}})
	case "appearance.get", "profile.get", "profile.update":
		o.ResponseSchema = schema(protocol.AppearanceSettings{})
	case "guest.configure":
		o.ResponseSchema = schema(guest.Settings{})
	case "guest.entry":
		o.ResponseSchema = responseObject([]string{"enabled"}, map[string]any{"enabled": map[string]any{"type": "boolean"}})
	case "guest.preview", "guest.display":
		o.ResponseSchema = schema(guest.Display{})
	case "core.operations":
		o.ResponseSchema = schema(Catalog{})
	case "core.protocol":
		o.ResponseSchema = schema(protocol.Descriptor{})
	case "auth.me":
		o.ResponseSchema = wrapped("user", responseObject([]string{"id", "username", "displayName", "role", "enabled"}, map[string]any{"id": map[string]any{"type": "integer"}, "username": text, "displayName": text, "role": text, "enabled": map[string]any{"type": "boolean"}}))
	case "accounts.list":
		o.ResponseSchema = list("accounts", map[string]any{"type": "object"})
	case "auth.login", "auth.setup", "auth.switch":
		o.ResponseSchema = responseObject([]string{"user", "csrfToken"}, map[string]any{"user": map[string]any{"type": "object"}, "csrfToken": text})
	case "sessions.list":
		o.ResponseSchema = list("sessions", map[string]any{"type": "object"})
	case "locations.search":
		o.ResponseSchema = list("cities", schema(geolocation.City{}))
	case "locations.get":
		o.ResponseSchema = schema(geolocation.City{})
	case "nodes.preferences":
		o.ResponseSchema = list("preferences", map[string]any{"type": "object"})
	case "alerts.list", "alerts.history":
		o.ResponseSchema = list("alerts", schema(alerts.Alert{}))
	case "alerts.events":
		o.ResponseSchema = list("events", schema(alerts.Event{}))
	case "rules.list", "rules.defaults":
		o.ResponseSchema = list("rules", schema(alerts.Rule{}))
	case "rules.create", "rules.update":
		o.ResponseSchema = wrapped("rule", schema(alerts.Rule{}))
	case "channels.options":
		o.ResponseSchema = list("providers", schema(alerts.ChannelProvider{}))
	case "channels.list":
		o.ResponseSchema = list("channels", schema(alerts.Channel{}))
	case "channels.create", "channels.update":
		o.ResponseSchema = wrapped("channel", schema(alerts.Channel{}))
	case "windows.list":
		o.ResponseSchema = list("windows", schema(alerts.Window{}))
	case "windows.create":
		o.ResponseSchema = wrapped("window", schema(alerts.Window{}))
	case "probes.list":
		o.ResponseSchema = list("probes", schema(probes.Config{}))
	case "probes.get", "probes.create", "probes.update":
		o.ResponseSchema = schema(probes.Config{})
	case "probes.history":
		o.ResponseSchema = list("runs", schema(probes.Run{}))
	case "commands.batches":
		o.ResponseSchema = list("batches", map[string]any{"type": "object"})
	case "updates.status":
		j := schema(update.Job{}).(map[string]any)
		j["required"] = []string{"id", "status"}
		o.ResponseSchema = map[string]any{"anyOf": []any{j, list("jobs", j)}}
	case "dashboard.core-info":
		o.ResponseSchema = schema(dashboard.CoreInfo{})
	case "dashboard.memo", "dashboard.save-memo":
		o.ResponseSchema = schema(dashboard.Memo{})
	case "core.health":
		o.ResponseSchema = responseObject([]string{"status", "version"}, map[string]any{"status": text, "version": text})
	case "auth.csrf":
		o.ResponseSchema = responseObject([]string{"token"}, map[string]any{"token": text})
	case "nodes.asset", "nodes.save-asset":
		o.ResponseSchema = schema(protocol.NodeAsset{})
	case "agents.settings", "agents.configure", "agents.diagnostics", "commands.get", "commands.submit", "commands.cancel", "commands.list", "commands.cleanup":
		r := schema(protocol.HostResponse{}).(map[string]any)
		r["required"] = []string{"status"}
		r["properties"].(map[string]any)["job"] = job
		if o.Name == "commands.submit" || o.Name == "commands.get" || o.Name == "commands.cancel" {
			r["required"] = []string{"status", "job"}
		}
		o.ResponseSchema = r
	case "containers.create", "containers.action", "images.pull", "images.delete", "compose.create", "compose.action", "files.action", "files.upload", "tasks.get", "tasks.cancel":
		o.ResponseSchema = task
	case "tasks.list":
		o.ResponseSchema = responseObject([]string{"tasks"}, map[string]any{"tasks": map[string]any{"type": "array", "items": task}, "nextCursor": text})
	case "updates.release":
		o.ResponseSchema = schema(update.Release{})
	case "updates.check":
		o.ResponseSchema = schema(update.Check{})
	case "updates.apply", "updates.rollback", "updates.delete-backup":
		r := schema(update.Job{}).(map[string]any)
		r["required"] = []string{"id", "status"}
		o.ResponseSchema = r
	case "nodes.list":
		o.ResponseSchema = responseObject([]string{"nodes"}, map[string]any{"nodes": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"country": schema(geolocation.Country{}), "location": schema(geolocation.Location{})}}}})
	case "terminal.create", "terminal.open", "terminal.resume":
		o.ResponseSchema = responseObject([]string{"streamId", "ticket"}, map[string]any{"streamId": text, "ticket": text, "expiresAt": text})
	}
}
