package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/control"
	"io"
	"net"
	"net/http"
)

const maxJSONRequest = 16 * 1024

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	limit := int64(maxJSONRequest)
	if operation, ok := control.MatchRoute(r.Method, r.URL.Path); ok && operation.MaxBodyBytes > 0 {
		limit = operation.MaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			status = http.StatusRequestTimeout
			w.Header().Set("Connection", "close")
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid request", status)
		return false
	}
	if operation, ok := control.MatchRoute(r.Method, r.URL.Path); ok {
		if err := control.ValidateJSON(operation.BodySchema, raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		status := http.StatusBadRequest
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			status = http.StatusRequestTimeout
			w.Header().Set("Connection", "close")
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid request", status)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}
