package control

import (
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// Request tags describe input presence; encoding/json omission describes output
// encoding and must not be used as request validation.
func describeTransport(o *Operation) {
	if o.Transport == "json" || o.Transport == "terminal" {
		o.ResponseSchema = map[string]any{"type": "object"}
	}
	if o.BodySchema != nil {
		o.MaxBodyBytes = 16 * 1024
	}
	if o.Idempotent {
		o.Headers = map[string]any{"Idempotency-Key": map[string]any{"type": "string", "minLength": 1, "description": "Reuse for retries of the same intent"}}
	}
	if o.Transport == "multipart" {
		o.Upload = &UploadContract{Field: protocol.AppearanceImageField, ContentType: "multipart/form-data", MaxBytes: protocol.MaxAppearanceImageBytes}
	}
	if o.Transport == "terminal" || o.Name == "terminal.connect" {
		o.Duplex = true
	}
	if o.Transport == "upload" {
		o.Upload = &UploadContract{ContentType: "application/octet-stream", MaxBytes: protocol.DefaultFileLimit}
		o.Headers["X-File-Version"] = map[string]any{"type": "string", "description": "Expected existing file version; omit only for an unconditional write"}
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
	case "commands.submit", "commands.batch":
		o.MaxBodyBytes = protocol.MaxCommandRequestBytes
		require("script", "timeoutSeconds", "confirm")
		if o.Name == "commands.batch" {
			require("nodeIds", "script", "timeoutSeconds", "confirm")
		}
		field("script", map[string]any{"minLength": 1, "maxBytes": protocol.MaxCommandScriptBytes})
		field("timeoutSeconds", map[string]any{"minimum": protocol.MinCommandTimeoutSeconds, "maximum": protocol.MaxCommandTimeoutSeconds})
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
	case "windows.create":
		require("kind", "scopeType", "startsAt", "endsAt")
	case "files.action":
		o.MaxBodyBytes = 6*protocol.MaxTextFileBytes + 8*1024
		field("content", map[string]any{"maxBytes": protocol.MaxTextFileBytes})
	case "compose.create", "compose.validate", "compose.action":
		o.MaxBodyBytes = 6*protocol.MaxComposeFileBytes + 16*1024
		field("content", map[string]any{"maxBytes": protocol.MaxComposeFileBytes})
	case "updates.apply", "updates.rollback":
		o.MaxBodyBytes = 65536
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
	case "deployments.create":
		completion("deployments.get", "taskId", "data.taskId", "status")
	case "updates.apply", "updates.rollback":
		completion("updates.status", "", "", "status")
		o.Completion.Params = map[string]string{}
		o.Completion.Query = map[string]string{"component": "body.component", "nodeId": "body.nodeId", "jobId": "data.id"}
	}
}

// MatchRoute shares published request bounds with the Core decoders. Multiple
// presentation names (terminal.open/create) can describe the same route.
func MatchRoute(method, path string) (Operation, bool) {
	for _, o := range operationCatalog() {
		if method != o.Method {
			continue
		}
		pattern, parts := strings.Split(o.Path, "/"), strings.Split(path, "/")
		if len(pattern) != len(parts) {
			continue
		}
		match := true
		for i, p := range pattern {
			if strings.HasPrefix(p, "{") {
				if parts[i] == "" {
					match = false
				}
			} else if p != parts[i] {
				match = false
			}
		}
		if match {
			return cloneOperation(o), true
		}
	}
	return Operation{}, false
}
