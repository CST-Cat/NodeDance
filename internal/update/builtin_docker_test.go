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

var workerFixtureVersion = "core-v0.1.8"
var workerFixtureImage = ""

// Only the isolated test executable has this fixture entrypoint. Product
// binaries never accept fixture settings or a replacement release resolver.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "__update-host" || os.Args[1] == "__update-rpc") {
		factory := func(config Config, resolver Resolver) (*Manager, error) {
			manager, err := New(config, resolver)
			if err != nil {
				return nil, err
			}
			manager.healthTimeout = 3 * time.Second
			if config.Targets["core"].Mode == "systemd" {
				manager.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if name == "systemctl" {
						return nil, nil
					}
					return execute(ctx, name, args...)
				}
			}

			manager.Docker.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body string
				if strings.HasSuffix(r.URL.Path, "/images/create") {
					body = `{"status":"isolated fixture image"}` + "\n"
				} else if strings.Contains(r.URL.Path, "/images/ghcr.io") {
					raw, err := execute(r.Context(), "docker", "image", "inspect", "--format", "{{.Id}}", workerFixtureImage)
					if err != nil {
						return nil, err
					}
					value, _ := json.Marshal(map[string]string{"Id": strings.TrimSpace(string(raw))})
					body = string(value)
				} else {
					return NewDocker().HTTP.Transport.RoundTrip(r)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			return manager, nil
		}
		_, err := internal(os.Args[1:], os.Stdout, os.Stderr, fixtureResolver{}, factory)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("NodeDance", workerFixtureVersion)
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.WriteFile("/var/lib/nodedance/runtime", []byte(workerFixtureVersion), 0600)
		http.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
			if os.Getenv("FIXTURE_FAIL") == "1" && workerFixtureVersion == "core-v0.1.9" {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": workerFixtureVersion})
		})
		if err := http.ListenAndServe(":8180", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBuiltInDockerWorkerSurvivesTargetReplacementWithoutInstalledService(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("NODEDANCE_TEST_DOCKER_UPDATE") != "1" {
		t.Skip("isolated rootful Docker update fixture is opt-in")
	}
	root := t.TempDir()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	oldImage := "nodedance-builtin-fixture:" + suffix + "-old"
	nextImage := "nodedance-builtin-fixture:" + suffix + "-new"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, entry := range []struct{ version, image string }{{"core-v0.1.8", oldImage}, {"core-v0.1.9", nextImage}} {
		binary := filepath.Join(root, "nodedance")
		flags := "-X github.com/CST-Cat/NodeDance/internal/update.workerFixtureVersion=" + entry.version + " -X github.com/CST-Cat/NodeDance/internal/update.workerFixtureImage=" + nextImage
		if raw, err := execute(ctx, "env", "CGO_ENABLED=0", "go", "test", "-c", "-ldflags", flags, "-o", binary, "."); err != nil {
			t.Fatalf("worker fixture compile: %s %v", raw, err)
		}
		source := "FROM debian:bookworm-slim\nRUN apt-get update && apt-get install -y --no-install-recommends util-linux && rm -rf /var/lib/apt/lists/*\nCOPY nodedance /usr/local/bin/nodedance\nUSER 0:0\nENTRYPOINT [\"/usr/local/bin/nodedance\"]\nCMD [\"serve\"]\n"
		os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(source), 0600)
		if raw, err := execute(ctx, "docker", "build", "-q", "-t", entry.image, root); err != nil {
			t.Fatalf("worker image: %s %v", raw, err)
		}
		image := entry.image
		t.Cleanup(func() { execute(context.Background(), "docker", "image", "rm", image) })
	}
	for _, scenario := range []string{"success", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			data := filepath.Join(root, scenario)
			os.Mkdir(data, 0700)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			name := "nodedance-builtin-fixture-" + scenario + "-" + suffix
			fail := "0"
			if scenario == "failure" {
				fail = "1"
			}
			if raw, err := execute(ctx, "docker", "run", "-d", "--name", name, "--read-only", "--cap-drop=ALL", "-p", fmt.Sprintf("127.0.0.1:%d:8180", port), "-v", data+":/var/lib/nodedance", "-e", "FIXTURE_FAIL="+fail, oldImage); err != nil {
				t.Fatalf("product fixture: %s %v", raw, err)
			}
			t.Cleanup(func() {
				raw, _ := execute(context.Background(), "docker", "ps", "-aq", "--filter", "name="+name)
				for _, id := range strings.Fields(string(raw)) {
					execute(context.Background(), "docker", "rm", "-f", id)
				}
			})
			engine := NewDocker()
			if err := engine.initialize(ctx); err != nil {
				t.Fatal(err)
			}
			product, err := engine.inspect(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			config := Config{StateDir: filepath.Join(root, "state-"+scenario), Targets: map[string]Target{"core": {Mode: "docker", Container: name, DataDir: data, HealthURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port)}}}
			cfg, _ := json.Marshal(config)
			checkRPC, _ := json.Marshal(RPC{Action: "check", Request: Request{Component: "core"}})
			checked, err := engine.worker(ctx, product, "core", string(cfg), string(checkRPC))
			if err != nil {
				t.Fatal(err)
			}
			var check Check
			if checked.Status != 200 || json.Unmarshal(checked.Data, &check) != nil || check.Current != "core-v0.1.8" || !check.Configured || !check.Available {
				t.Fatalf("same-binary Docker preflight: %+v / %+v", checked, check)
			}
			rpc, _ := json.Marshal(RPC{Action: "apply", Request: Request{Component: "core", Confirm: true}, IdempotencyKey: "isolated-built-in-" + scenario})
			response, err := engine.worker(ctx, product, "core", string(cfg), string(rpc))
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != 202 || response.Error != "" {
				t.Fatalf("worker acceptance: %+v", response)
			}
			var accepted Job
			json.Unmarshal(response.Data, &accepted)
			deadline := time.Now().Add(30 * time.Second)
			var final Job
			for time.Now().Before(deadline) {
				raw, err := os.ReadFile(filepath.Join(config.StateDir, accepted.ID+".json"))
				if err == nil {
					var record journal
					if json.Unmarshal(raw, &record) == nil {
						final = record.job()
						if final.Finished != nil {
							break
						}
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			want := "succeeded"
			if scenario == "failure" {
				want = "rolled_back"
			}
			if final.Status != want {
				t.Fatalf("detached worker result: %+v", final)
			}
			current, err := engine.inspect(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" && current["Id"] == product["Id"] {
				t.Fatal("target was not replaced")
			}
			if scenario == "failure" && current["Id"] != product["Id"] {
				t.Fatal("failed update did not restore original target")
			}
		})
	}
}

func TestBuiltInNativeWorkerRunsInTransientUnitWithoutTouchingHostServices(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("NODEDANCE_TEST_DOCKER_UPDATE") != "1" {
		t.Skip("isolated native worker fixture is opt-in")
	}
	manager, _, _ := nativeFixture(t)
	config := manager.Config
	manager.Close()
	cfg, _ := json.Marshal(config)
	rpc, _ := json.Marshal(RPC{Action: "apply", Request: Request{Component: "core", Confirm: true}, IdempotencyKey: "isolated-native-built-in"})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := launchNative(ctx, binary, string(cfg), string(rpc))
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != 202 || response.Error != "" {
		t.Fatalf("native worker acceptance: %+v", response)
	}
	var accepted Job
	if err = json.Unmarshal(response.Data, &accepted); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		raw, err := os.ReadFile(filepath.Join(config.StateDir, accepted.ID+".json"))
		if err == nil {
			var record journal
			if json.Unmarshal(raw, &record) == nil {
				job := record.job()
				if job.Finished != nil {
					if job.Status != "succeeded" {
						t.Fatalf("native worker result: %+v", job)
					}
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native transient worker did not complete")
}
