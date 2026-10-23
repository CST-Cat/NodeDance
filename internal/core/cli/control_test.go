package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestControlCLI(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/operations" {
			json.NewEncoder(w).Encode(control.Describe())
			return
		}
		if r.URL.Path == "/api/v1/nodes" {
			io.WriteString(w, `{"nodes":[]}`)
			return
		}
		http.Error(w, "unavailable", 503)
	}))
	defer host.Close()
	lookup := func(string) (string, bool) { return "", false }
	var out, stderr strings.Builder
	args := []string{"nodes", "list", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}
	if err := RunWithInput(context.Background(), args, lookup, strings.NewReader(""), &out, &stderr, "test"); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Operation string `json:"operation"`
		Status    int    `json:"status"`
	}
	if json.Unmarshal([]byte(out.String()), &result) != nil || result.Operation != "nodes.list" || result.Status != 200 {
		t.Fatal(out.String())
	}
	out.Reset()
	path := filepath.Join(t.TempDir(), "download")
	args = []string{"call", "files.download", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client"), "--params", `{"nodeId":"node"}`, "--query", `{"path":"/tmp/a"}`, "--output", path}
	if err := RunWithInput(context.Background(), args, lookup, strings.NewReader(""), &out, &stderr, "test"); err == nil {
		t.Fatal("failed download reported success")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial download left behind")
	}
	os.WriteFile(path, []byte("keep"), 0600)
	if err := RunWithInput(context.Background(), args, lookup, strings.NewReader(""), &out, &stderr, "test"); err == nil {
		t.Fatal("existing output accepted")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "keep" {
		t.Fatal("existing download overwritten")
	}
	out.Reset()
	if err := runOperations(nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"terminal.open", "files.upload", "agent.enroll", "core.backup", "channels.test"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestLocalizedCatalogKeepsMachineContract(t *testing.T) {
	var english, chinese bytes.Buffer
	lookup := func(string) (string, bool) { return "", false }
	for _, entry := range []struct {
		language string
		output   *bytes.Buffer
	}{{"en", &english}, {"zh-CN", &chinese}} {
		if err := RunWithInput(context.Background(), []string{"operations", "nodes.list", "--lang", entry.language}, lookup, strings.NewReader(""), entry.output, io.Discard, "test"); err != nil {
			t.Fatal(err)
		}
	}
	var en, zh control.Operation
	if err := json.Unmarshal(english.Bytes(), &en); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(chinese.Bytes(), &zh); err != nil {
		t.Fatal(err)
	}
	if zh.Description == en.Description || !strings.Contains(zh.Description, "节点") {
		t.Fatal(zh.Description)
	}
	zh.Description = en.Description
	if !reflect.DeepEqual(en, zh) {
		t.Fatal("localization changed machine contract")
	}
}

func TestCLIEnrollmentSelectsDeploymentMethod(t *testing.T) {
	for _, method := range []string{"native", "docker"} {
		t.Run(method, func(t *testing.T) {
			var received map[string]string
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/operations" {
					json.NewEncoder(w).Encode(control.Describe())
					return
				}
				switch r.URL.Path {
				case "/api/v1/auth/csrf":
					http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
					io.WriteString(w, `{"token":"csrf"}`)
				case "/api/v1/agents/enrollments":
					if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
						t.Error(err)
					}
					io.WriteString(w, `{"installCommand":"selected command","token":"one-time"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer host.Close()
			var out strings.Builder
			args := []string{"agents", "enrollment", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client"), "--body", `{"displayName":"node","coreUrl":"https://core.example"}`, "--deployment-method", method}
			if err := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader(""), &out, io.Discard, "test"); err != nil {
				t.Fatal(err)
			}
			if received["deploymentMethod"] != method || received["displayName"] != "node" || received["coreUrl"] != "https://core.example" {
				t.Fatalf("incorrect request: %v", received)
			}
			if !strings.Contains(out.String(), "selected command") {
				t.Fatal(out.String())
			}
		})
	}
	var out strings.Builder
	err := RunWithInput(context.Background(), []string{"agents", "enrollment", "--body", `{"displayName":"node"}`, "--deployment-method", "unsupported"}, func(string) (string, bool) { return "", false }, strings.NewReader(""), &out, io.Discard, "test")
	if err == nil || !strings.Contains(err.Error(), "native or docker") {
		t.Fatalf("unsupported method accepted: %v", err)
	}
}

func TestCLIUsesOperationAddedOnlyByConnectedCore(t *testing.T) {
	operation := control.Operation{Name: "future.inspect", Method: "GET", Path: "/api/v1/future/{id}", Parameters: []string{"id"}, Query: []string{}, Transport: "json", Description: "Server-only operation"}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{operation}})
		case "/api/v1/future/new":
			io.WriteString(w, `{"discovered":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	if _, err := control.Find(operation.Name); err == nil {
		t.Fatal("fixture operation unexpectedly exists in client")
	}
	lookup := func(string) (string, bool) { return "", false }
	for _, command := range [][]string{{"call", operation.Name, "--params", `{"id":"new"}`}, {"operations", operation.Name}} {
		args := append(command, "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client"))
		var output strings.Builder
		if err := RunWithInput(context.Background(), args, lookup, strings.NewReader(""), &output, io.Discard, "test"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), operation.Name) {
			t.Fatal("server operation was not discovered")
		}
	}
}

func TestOperationInputExceedsOldFourMiBBound(t *testing.T) {
	payload := strings.Repeat("x", 5<<20)
	source := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(source, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"-", "@" + source} {
		result, err := readOperationValue(input, strings.NewReader(payload), 6<<20)
		if err != nil || string(result) != payload {
			t.Fatal("shared terminal input truncated", len(result), err)
		}
		if _, err := readOperationValue(input, strings.NewReader(payload), 4<<20); err == nil {
			t.Fatal("operation bound not enforced")
		}
	}
}

func TestStreamJSONLPreservesNumbersAndRejectsMalformedFrames(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		for _, frame := range []string{` { "bytes": 18446744073709551615 } `, `{`} {
			if err := connection.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output strings.Builder
	err = streamJSONL(ctx, client, control.Operation{Name: "stream", Path: "/ws/stream", Method: "GET", Transport: "websocket"}, control.Input{}, nil, &output)
	if err == nil || err.Error() != "Core returned invalid stream JSON" {
		t.Fatalf("Malformed frame: %v", err)
	}
	if output.String() != "{\"bytes\":18446744073709551615}\n" {
		t.Fatalf("Output lost exact JSON: %q", output.String())
	}
}
