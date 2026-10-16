package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This fixture entrypoint is present only in the isolated test executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__update-rpc" {
		factory := func(config Config, resolver Resolver) (*Manager, error) {
			manager, err := New(config, resolver)
			if err != nil {
				return nil, err
			}
			if config.Targets["core"].Mode == "systemd" {
				manager.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if name == "systemctl" {
						return nil, nil
					}
					return execute(ctx, name, args...)
				}
			}
			return manager, nil
		}
		if _, err := internal(os.Args[1:], os.Stdout, os.Stderr, fixtureResolver{}, factory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBuiltInNativeWorkerRunsInTransientUnitWithoutTouchingHostServices(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("NODEDANCE_TEST_SYSTEMD_UPDATE") != "1" {
		t.Skip("isolated native update worker fixture is opt-in")
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
