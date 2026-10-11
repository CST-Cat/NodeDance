package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestControlCLI(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
