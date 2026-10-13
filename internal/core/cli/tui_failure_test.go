package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
)

func TestTUIBodyReadFailureDoesNotSubmit(t *testing.T) {
	var submits atomic.Int32
	o := control.Operation{Name: "future.optional-body", Method: "POST", Path: "/api/v1/optional-body", Transport: "json", BodySchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{o}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case o.Path:
			submits.Add(1)
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/future.optional-body"}, {"Filter: future.optional-body", "1"}, {"JSON body or @private-file", "@" + filepath.Join(t.TempDir(), "missing.json")}, {"Enter to return:", ""}, {"Select:", "q"}})
	if submits.Load() != 0 {
		t.Fatalf("failed body read submitted %d requests", submits.Load())
	}
}
