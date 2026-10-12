package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func valueAt(value any, path string) any {
	if path == "" {
		return value
	}
	for _, key := range strings.Split(path, ".") {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = obj[key]
	}
	return value
}

// CompletionInput resolves follow-up requests from the connected Core contract.
func CompletionInput(o Operation, in Input, accepted Result) (string, Input, error) {
	if o.Completion == nil {
		return "", Input{}, fmt.Errorf("operation has no completion contract")
	}
	var data, body any
	if err := json.Unmarshal(accepted.Data, &data); err != nil {
		return "", Input{}, err
	}
	if len(in.Body) > 0 {
		if err := json.Unmarshal(in.Body, &body); err != nil {
			return "", Input{}, err
		}
	}
	root := map[string]any{"data": data, "body": body, "params": stringValues(in.Params), "query": stringValues(in.Query)}
	result := Input{Params: map[string]string{}, Query: map[string]string{}}
	for key, ref := range o.Completion.Params {
		v, ok := valueAt(root, ref).(string)
		if !ok || v == "" {
			return "", Input{}, fmt.Errorf("Core completion reference %s is missing", ref)
		}
		result.Params[key] = v
	}
	for key, ref := range o.Completion.Query {
		if v, ok := valueAt(root, ref).(string); ok && v != "" {
			result.Query[key] = v
		}
	}
	return o.Completion.Operation, result, nil
}

func stringValues(values map[string]string) map[string]any {
	result := map[string]any{}
	for k, v := range values {
		result[k] = v
	}
	return result
}

// Wait observes existing work. It never resubmits the original operation.
// Unknown/canceled/failed are terminal outcomes, not transport errors to replay.
func (c *Client) Wait(ctx context.Context, catalog Catalog, o Operation, in Input, accepted Result, observe func(Result)) Result {
	if accepted.Error != "" || o.Completion == nil {
		return accepted
	}
	name, next, err := CompletionInput(o, in, accepted)
	if err != nil {
		accepted.Error = err.Error()
		return accepted
	}
	follow, err := FindIn(catalog, name)
	if err != nil {
		accepted.Error = err.Error()
		return accepted
	}
	for {
		result := c.Execute(ctx, follow, next, nil, nil)
		if observe != nil {
			observe(result)
		}
		if result.Error == "" {
			var data any
			if json.Unmarshal(result.Data, &data) != nil {
				result.Error = "invalid completion response"
				return result
			}
			status, _ := valueAt(data, o.Completion.StatusPath).(string)
			if status == "" {
				result.Error = "Core completion response has no status"
				return result
			}
			for _, terminal := range o.Completion.TerminalStatuses {
				if status == terminal {
					for _, success := range o.Completion.SuccessStatuses {
						if status == success {
							return result
						}
					}
					result.Error = "operation finished with status " + status
					return result
				}
			}
		} else if result.Status >= 400 && result.Status < 500 {
			return result
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.Error = "completion wait stopped; submitted work was not canceled: " + ctx.Err().Error()
			return result
		case <-timer.C:
		}
	}
}
