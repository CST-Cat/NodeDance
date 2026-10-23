package update

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"time"
)

func (m *Manager) Handle(ctx context.Context, rpc RPC) Response {
	if rpc.Action == "tailscale-peers" {
		// This fixed read never accepts a command, path, target node or update options.
		if rpc.Request != (Request{Component: "core"}) || rpc.IdempotencyKey != "" {
			return Response{Status: 400, Error: "host discovery requires only component core"}
		}
		if _, ok := m.Config.Targets["core"]; !ok {
			return Response{Status: 403, Error: "Core is not registered on this host"}
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		peers, err := (tailscale.Discovery{Runner: m.tailscaleRunner}).List(ctx)
		if errors.Is(err, tailscale.ErrUnavailable) {
			return Response{Status: 404, Error: "Tailscale is unavailable on the deployment host"}
		}
		if errors.Is(err, tailscale.ErrNotLoggedIn) {
			return Response{Status: 412, Error: "Deployment host is not signed in to Tailscale"}
		}
		if err != nil {
			return Response{Status: 503, Error: "Cannot read deployment host Tailscale status"}
		}
		raw, err := json.Marshal(peers)
		if err != nil || len(raw) > 900<<10 {
			return Response{Status: 502, Error: "Host discovery response exceeds its limit"}
		}
		return Response{Status: 200, Data: raw}
	}
	if err := Validate(rpc.Request); err != nil {
		return Response{Status: 400, Error: err.Error()}
	}
	var data any
	var err error
	status := 200
	switch rpc.Action {
	case "check":
		data, err = m.Check(ctx, rpc.Request)
	case "apply", "rollback":
		if rpc.Action == "apply" && rpc.Request.JobID != "" {
			return Response{Status: 400, Error: "use rollback for a completed update"}
		}
		if rpc.Action == "rollback" && rpc.Request.JobID == "" {
			return Response{Status: 400, Error: "rollback requires jobId"}
		}
		data, err = m.Apply(ctx, rpc.Request, rpc.IdempotencyKey)
		status = 202
	case "delete-backup":
		data, err = m.DeleteBackup(ctx, rpc.Request)
	case "status":
		if rpc.Request.JobID == "" {
			jobs := []Job{}
			for _, job := range m.Jobs() {
				if job.Component == rpc.Request.Component {
					jobs = append(jobs, job)
				}
			}
			data = map[string]any{"jobs": jobs}
		} else {
			data, err = m.Get(rpc.Request.Component, rpc.Request.JobID)
		}
	default:
		return Response{Status: 400, Error: "unknown update operation"}
	}
	if err != nil {
		return Response{Status: 409, Error: err.Error()}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Response{Status: 500, Error: "cannot encode update state"}
	}
	return Response{Status: status, Data: raw}
}
