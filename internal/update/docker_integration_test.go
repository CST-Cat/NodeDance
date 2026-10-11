package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in: uses only its own temporary containers/images/data. The real Docker
// daemon performs replacement and recovery; the transport substitutes a local
// test image for the release pull, without adding a product bypass flag.
func TestDockerTransactionalUpdate(t *testing.T) {
	if os.Getenv("NODEDANCE_TEST_DOCKER_UPDATE") != "1" || os.Geteuid() != 0 {
		t.Skip("opt-in rootful Docker transaction integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	source := `package main
import("fmt";"net/http";"os")
var version string
func main(){if len(os.Args)>1 && (os.Args[1]=="version" || os.Args[1]=="--version"){fmt.Println("NodeDance",version);return};os.WriteFile("/var/lib/nodedance/runtime",[]byte(version),0600);http.HandleFunc("/api/v1/health",func(w http.ResponseWriter,r *http.Request){if os.Getenv("FAIL_HEALTH")=="1" && version=="core-v0.1.9"{w.WriteHeader(503);return};fmt.Fprintf(w,"{\"status\":\"ok\",\"version\":\"%s\"}",version)});http.ListenAndServe(":8180",nil)}
`
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	images := []string{}
	for _, version := range []string{"core-v0.1.8", "core-v0.1.9"} {
		binary := filepath.Join(root, "nodedance")
		if raw, err := execute(ctx, "env", "CGO_ENABLED=0", "go", "build", "-ldflags", "-X main.version="+version, "-o", binary, filepath.Join(root, "fixture.go")); err != nil {
			t.Fatalf("fixture build: %s %v", raw, err)
		}
		dockerfile := []byte("FROM scratch\nCOPY nodedance /nodedance\nUSER 10001:10001\nENV PATH=/\nENTRYPOINT [\"nodedance\"]\nCMD [\"serve\"]\n")
		os.WriteFile(filepath.Join(root, "Dockerfile"), dockerfile, 0600)
		image := "nodedance-update-test:" + strings.TrimPrefix(version, "core-v") + "-" + filepath.Base(root)
		if raw, err := execute(ctx, "docker", "build", "-q", "-t", image, root); err != nil {
			t.Fatalf("fixture image: %s %v", raw, err)
		}
		images = append(images, image)
	}
	t.Cleanup(func() {
		for _, image := range images {
			execute(context.Background(), "docker", "image", "rm", image)
		}
	})
	for _, scenario := range []string{"success", "failure", "interrupted"} {
		failure := scenario == "failure"
		t.Run(scenario, func(t *testing.T) {
			data := filepath.Join(root, "data-"+scenario)
			state := filepath.Join(root, "state-"+scenario)
			os.Mkdir(data, 0700)
			os.Chown(data, 10001, 10001)
			os.Mkdir(state, 0700)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			name := fmt.Sprintf("nodedance-update-test-%d", port)
			env := "FAIL_HEALTH=0"
			if failure {
				env = "FAIL_HEALTH=1"
			}
			if raw, err := execute(ctx, "docker", "run", "-d", "--name", name, "--read-only", "--cap-drop=ALL", "--restart", "unless-stopped", "--publish", fmt.Sprintf("127.0.0.1:%d:8180", port), "--mount", "type=bind,src="+data+",dst=/var/lib/nodedance", "--env", env, images[0]); err != nil {
				t.Fatalf("fixture run: %s %v", raw, err)
			}
			t.Cleanup(func() {
				raw, _ := execute(context.Background(), "docker", "ps", "-aq", "--filter", "name="+name)
				for _, id := range strings.Fields(string(raw)) {
					execute(context.Background(), "docker", "rm", "-f", id)
				}
			})
			m, err := New(Config{StateDir: state, Targets: map[string]Target{"core": {Mode: "docker", Container: name, DataDir: data, HealthURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)}}}, fixtureResolver{})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.healthTimeout = 300 * time.Millisecond
			// Intercept only release pull/inspection; all container API calls are real.
			imageID, err := execute(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", images[1])
			if err != nil {
				t.Fatal(err)
			}
			original := m.Docker.HTTP.Transport
			m.Docker.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body string
				if strings.HasSuffix(r.URL.Path, "/images/create") {
					body = `{"status":"local fixture pulled"}` + "\n"
				} else if strings.Contains(r.URL.Path, "/images/ghcr.io") {
					raw, _ := json.Marshal(map[string]string{"Id": strings.TrimSpace(string(imageID))})
					body = string(raw)
				} else {
					return original.RoundTrip(r)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			if err = m.verify(ctx, "core", "core-v0.1.8", 0); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(data, "credential"), []byte("retained-private-state"), 0600)
			os.Chown(filepath.Join(data, "credential"), 10001, 10001)
			job, err := m.Apply(ctx, Request{Component: "core", Confirm: true}, "docker-update-key")
			if err != nil {
				t.Fatal(err)
			}
			job = waitJob(t, m, job.ID)
			expected := "succeeded"
			if failure {
				expected = "rolled_back"
			}
			if job.Status != expected {
				t.Fatalf("%+v", job)
			}
			installed := "core-v0.1.9"
			if failure {
				installed = "core-v0.1.8"
			}
			if err = m.verify(ctx, "core", installed, 0); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(data, "credential"))
			if string(raw) != "retained-private-state" {
				t.Fatal("state lost during replacement")
			}
			current, err := m.Docker.inspect(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			host := current["HostConfig"].(map[string]any)
			config := current["Config"].(map[string]any)
			if config["User"] != "10001:10001" || host["ReadonlyRootfs"] != true || host["RestartPolicy"].(map[string]any)["Name"] != "unless-stopped" {
				t.Fatal("deployment security/settings changed")
			}
			if scenario == "interrupted" {
				job.Status = "running"
				job.Stage = "verifying"
				job.Finished = nil
				m.mu.Lock()
				err = m.persist(job)
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				config := m.Config
				m.Close()
				recovered, err := New(config, fixtureResolver{})
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Close()
				recoveredJob := waitJob(t, recovered, job.ID)
				if recoveredJob.Status != "rolled_back" {
					t.Fatalf("interrupted recovery: %+v", recoveredJob)
				}
				if err = recovered.verify(ctx, "core", "core-v0.1.8", 0); err != nil {
					t.Fatal(err)
				}
			} else if !failure {
				rollback, err := m.Apply(ctx, Request{Component: "core", Confirm: true, JobID: job.ID}, "docker-rollback-key")
				if err != nil {
					t.Fatal(err)
				}
				rollback = waitJob(t, m, rollback.ID)
				if rollback.Status != "succeeded" {
					t.Fatalf("%+v", rollback)
				}
				if err = m.verify(ctx, "core", "core-v0.1.8", 0); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
