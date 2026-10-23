package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (c *Client) ValidateInput(o Operation, in Input) error {
	if _, e := c.endpoint(o, in); e != nil {
		return e
	}
	if len(in.Body) > 0 && len(bytes.TrimSpace(in.Body)) == 0 {
		return errors.New("body must contain one JSON value")
	}
	if o.MaxBodyBytes > 0 && int64(len(in.Body)) > o.MaxBodyBytes {
		return errors.New("body exceeds Core request limit")
	}
	if e := ValidateJSON(o.BodySchema, in.Body); e != nil {
		return e
	}
	_, e := operationHeaders(o, in)
	return e
}
func operationHeaders(o Operation, in Input) (http.Header, error) {
	out := http.Header{}
	if o.Idempotent {
		key := in.IdempotencyKey
		for name, value := range in.Headers {
			if strings.EqualFold(name, "Idempotency-Key") {
				if key != "" && key != value {
					return nil, errors.New("conflicting idempotency keys")
				}
				key = value
			}
		}
		if key == "" {
			return nil, errors.New("--idempotency-key is required; reuse the same key when retrying the same intent")
		}
		raw, _ := json.Marshal(key)
		if e := ValidateJSON(o.Headers["Idempotency-Key"], raw); e != nil {
			return nil, e
		}
		out.Set("Idempotency-Key", key)
	}
	for name, value := range in.Headers {
		schema, ok := o.Headers[http.CanonicalHeaderKey(name)]
		if !ok {
			return nil, fmt.Errorf("unknown operation header %s", name)
		}
		raw, _ := json.Marshal(value)
		if e := ValidateJSON(schema, raw); e != nil {
			return nil, fmt.Errorf("%s: %w", name, e)
		}
		out.Set(name, value)
	}
	if in.FileVersion != "" {
		if _, ok := o.Headers["X-File-Version"]; !ok {
			return nil, errors.New("operation does not accept a file version")
		}
		out.Set("X-File-Version", in.FileVersion)
	}
	return out, nil
}
