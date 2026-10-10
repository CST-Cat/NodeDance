package tailscale

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type stage6Runner struct{}

func (stage6Runner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(`{"BackendState":"Running","Peer":{"peer-key":{"PublicKey":"peer-key","HostName":"stage6-test","OS":"linux","TailscaleIPs":["100.64.0.2"],"Online":true}}}`), nil
}

type stage6CountingRunner struct{ calls atomic.Int32 }

func (r *stage6CountingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls.Add(1)
	return []byte(`{"BackendState":"Running","Peer":{"peer-key":{"PublicKey":"peer-key","HostName":"stage6-test","OS":"linux","TailscaleIPs":["100.64.0.2"],"Online":true}}}`), nil
}

type stage6Artifacts struct{}

func (stage6Artifacts) Load(osName, arch string) (Artifact, error) {
	return Artifact{Bytes: []byte("local signed-artifact stand-in"), OS: osName, Arch: arch}, nil
}

type stage6Remote struct {
	preflight string
	commands  []string
	installs  []string
}

func (r *stage6Remote) Run(_ context.Context, command string, input []byte) ([]byte, error) {
	script := string(input)
	r.commands = append(r.commands, command+"\n"+script)
	switch {
	case strings.Contains(script, "uname -s"):
		return []byte(r.preflight), nil
	case strings.Contains(script, "curl --fail --silent"):
		return []byte("primary\n"), nil
	default:
		r.installs = append(r.installs, script)
		return nil, nil
	}
}

func (*stage6Remote) Close() error { return nil }

type stage6Transport struct{ remote Remote }

func (stage6Transport) Probe(context.Context, Peer) (string, error) { return "SHA256:probe", nil }
func (s stage6Transport) Connect(context.Context, Peer, SSHCredentials, string, *StateStore, bool) (Remote, error) {
	return s.remote, nil
}

func TestCoordinatorKeepsAgentDeploymentAvailableAcrossDockerStates(t *testing.T) {
	cases := []struct {
		name          string
		dockerLine    string
		waitAvailable bool
		wantDocker    string
		wantSuppGroup string
	}{
		{name: "no Docker socket", dockerLine: "docker|absent", wantDocker: "absent"},
		{name: "inaccessible Docker socket", dockerLine: "docker|600|1000|1000|0", wantDocker: "unavailable"},
		{name: "group-accessible Docker socket", dockerLine: "docker|660|0|999|0", waitAvailable: true, wantDocker: "connected", wantSuppGroup: "--supplementary-group '999'"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			remote := &stage6Remote{preflight: fmt.Sprintf("Linux\nx86_64\n0\nnew\n-\nroot\nunit|absent|-\n%s\n", testCase.dockerLine)}
			dataDir := t.TempDir()
			state := NewStateStore(dataDir)
			database, err := storage.Open(context.Background(), dataDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			taskStore, err := coretasks.New(database.DB, coretasks.Options{})
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{
				State: state,
				Dependencies: Dependencies{
					Discovery: Discovery{Runner: stage6Runner{}},
					Artifacts: stage6Artifacts{},
					Transport: stage6Transport{remote: remote},
					Create: func(ctx context.Context, _ string) (Enrollment, error) {
						if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
							return Enrollment{}, err
						}
						return Enrollment{NodeID: "node-1", Token: "one-time-token"}, nil
					},
					WaitOnline: func(context.Context, string) error { return nil },
					WaitDocker: func(context.Context, string) (bool, error) { return testCase.waitAvailable, nil },
				},
			}
			coordinator, err := SharedCoordinator(dataDir, service, taskStore)
			if err != nil {
				t.Fatal(err)
			}
			request := DeployRequest{
				PeerIdentity: "peer-key", DisplayName: "stage6-test", CoreURL: "https://core.example",
				HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", ConfirmHostKey: true,
				Credentials: SSHCredentials{User: "root", Password: "one-time-password"},
			}
			actor := sql.NullInt64{Int64: 1, Valid: true}
			task, err := coordinator.Start(context.Background(), request, "stage6-idempotency-key", actor, "127.0.0.1")
			if err != nil {
				t.Fatalf("start deployment: %v", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				current, ok := coordinator.Task(context.Background(), task.ID)
				if ok && current.FinishedAt != nil {
					if current.Status != "succeeded" || current.DockerState != testCase.wantDocker {
						t.Fatalf("unexpected completed task: status=%s docker=%s message=%q", current.Status, current.DockerState, current.Message)
					}
					if current.DockerState == "unavailable" && !strings.Contains(current.Message, "host monitoring is active") {
						t.Fatalf("Docker-unavailable result did not confirm host monitoring: %q", current.Message)
					}
					if len(remote.installs) != 1 {
						t.Fatalf("expected one Agent install, got %d", len(remote.installs))
					}
					retried, err := coordinator.Start(context.Background(), request, "stage6-idempotency-key", actor, "127.0.0.1")
					if err != nil || retried.ID != task.ID {
						t.Fatalf("same-key retry did not return the original task: task=%q err=%v", retried.ID, err)
					}
					changed := request
					changed.CoreURL = "https://different-core.example"
					if _, err := coordinator.Start(context.Background(), changed, "stage6-idempotency-key", actor, "127.0.0.1"); !errors.Is(err, coretasks.ErrIdempotencyConflict) {
						t.Fatalf("same key with different deployment intent should conflict, got %v", err)
					}
					if len(remote.installs) != 1 {
						t.Fatalf("idempotent retries started another SSH install: %d", len(remote.installs))
					}
					install := remote.installs[0]
					if strings.Contains(install, "chmod 666 /var/run/docker.sock") || strings.Contains(install, "chgrp") {
						t.Fatal("Agent deployment must not widen Docker socket permissions")
					}
					if strings.Contains(install, "--supplementary-group") != (testCase.wantSuppGroup != "") {
						t.Fatalf("unexpected Docker supplementary group setup in install script")
					}
					if testCase.wantSuppGroup != "" && !strings.Contains(install, testCase.wantSuppGroup) {
						t.Fatalf("install script does not preserve Docker socket group access %q", testCase.wantSuppGroup)
					}
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("deployment task did not reach a terminal state")
		})
	}
}

func TestExpiredDeploymentContextStillPersistsUnknownCoreTask(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:peer-key",
		AgentDeploy: &protocol.AgentDeployTaskSpec{PeerIdentity: "peer-key", PeerName: "stage6-test", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password"},
	}
	accepted, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-1", IdempotencyKey: "expired-deploy-key", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	executorCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if executorCtx.Err() == nil {
		t.Fatal("test executor context should be expired")
	}
	if err := persistDeploymentOutcome(store, accepted.Task, DeploymentResult{NodeID: "node-1", PeerIdentity: "peer-key"}, executorCtx.Err(), sql.NullInt64{}, "127.0.0.1"); err != nil {
		t.Fatalf("persist result after executor context expiration: %v", err)
	}
	current, err := store.Get(ctx, "node-1", accepted.Task.TaskID)
	if err != nil || current.Status != taskstate.Unknown || current.Result.Code != coretasks.ResultUncertain {
		t.Fatalf("expired operation result must not leave a running task: task=%#v err=%v", current, err)
	}
}

func TestCoordinatorRejectsUnresolvedPeerClaimBeforeEnrollment(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:peer-key",
		AgentDeploy: &protocol.AgentDeployTaskSpec{PeerIdentity: "peer-key", PeerName: "stage6-test", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password"},
	}
	accepted, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-1", IdempotencyKey: "unknown-deploy-key", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkLocalUnknown(ctx, "node-1", accepted.Task.TaskID, sql.NullInt64{}, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	enrollmentCalls := 0
	service := &Service{State: NewStateStore(dataDir), Dependencies: Dependencies{
		Create: func(context.Context, string) (Enrollment, error) {
			enrollmentCalls++
			return Enrollment{NodeID: "node-2", Token: "token"}, nil
		},
	}}
	coordinator, err := SharedCoordinator(dataDir, service, store)
	if err != nil {
		t.Fatal(err)
	}
	request := DeployRequest{PeerIdentity: "peer-key", DisplayName: "stage6-test", CoreURL: "https://core.example",
		HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", ConfirmHostKey: true,
		Credentials: SSHCredentials{User: "root", Password: "one-time-password"}}
	if _, err := coordinator.Start(ctx, request, "new-deploy-key", sql.NullInt64{Int64: 1, Valid: true}, "127.0.0.1"); !errors.Is(err, coretasks.ErrResourceBusy) {
		t.Fatalf("a second deployment for an unresolved peer should fail before SSH/enrollment, got %v", err)
	}
	if enrollmentCalls != 0 {
		t.Fatalf("unresolved peer claim created %d extra enrollment(s)", enrollmentCalls)
	}
}

func TestCoordinatorAssociationSharesSynchronizedDeploymentState(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	firstService := &Service{State: NewStateStore(dataDir)}
	first, err := SharedCoordinator(dataDir, firstService, store)
	if err != nil {
		t.Fatal(err)
	}
	secondService := &Service{State: NewStateStore(dataDir)}
	second, err := SharedCoordinator(dataDir, secondService, store)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || secondService.State != first.state {
		t.Fatal("deployment and task-resolution paths must share one synchronized StateStore")
	}
	var wait sync.WaitGroup
	errCh := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		errCh <- first.AssociatePeer("resolved-peer", "node-1")
	}()
	go func() {
		defer wait.Done()
		errCh <- secondService.State.Associate("deployment-peer", "node-2")
	}()
	wait.Wait()
	close(errCh)
	for writeErr := range errCh {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	state, err := NewStateStore(dataDir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Managed["resolved-peer"] != "node-1" || state.Managed["deployment-peer"] != "node-2" {
		t.Fatalf("concurrent shared-state writes lost an association: %#v", state.Managed)
	}
}

func TestCoordinatorTaskStoreAccessIsSynchronizedWithSharedRefresh(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:peer-key",
		AgentDeploy: &protocol.AgentDeployTaskSpec{PeerIdentity: "peer-key", PeerName: "stage6-test", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password"},
	}
	accepted, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-1", IdempotencyKey: "race-deploy-key", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkLocalUnknown(ctx, "node-1", accepted.Task.TaskID, sql.NullInt64{}, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	service := &Service{State: NewStateStore(dataDir)}
	coordinator, err := SharedCoordinator(dataDir, service, store)
	if err != nil {
		t.Fatal(err)
	}
	request := DeployRequest{PeerIdentity: "peer-key", DisplayName: "stage6-test", CoreURL: "https://core.example",
		HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", ConfirmHostKey: true,
		Credentials: SSHCredentials{User: "root", Password: "one-time-password"}}
	var wait sync.WaitGroup
	errCh := make(chan error, 4*40)
	wait.Add(4)
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			if _, err := SharedCoordinator(dataDir, &Service{State: NewStateStore(dataDir)}, store); err != nil {
				errCh <- err
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			if _, ok := coordinator.Task(ctx, accepted.Task.TaskID); !ok {
				errCh <- errors.New("Core task lookup failed during shared coordinator refresh")
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			if _, err := coordinator.UnresolvedTasks(ctx); err != nil {
				errCh <- err
			}
		}
	}()
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			if _, err := coordinator.Start(ctx, request, "another-deploy-key", sql.NullInt64{}, "127.0.0.1"); !errors.Is(err, coretasks.ErrResourceBusy) {
				errCh <- fmt.Errorf("deployment should stop on the existing peer claim, got %v", err)
			}
		}
	}()
	wait.Wait()
	close(errCh)
	for concurrentErr := range errCh {
		t.Error(concurrentErr)
	}
}

func TestCoordinatorStartSnapshotsServiceAndStateDuringSharedRefresh(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}

	state := NewStateStore(dataDir)
	if err := state.Pin("peer-key", "SHA256:previously-pinned-host-key"); err != nil {
		t.Fatal(err)
	}
	runner := &stage6CountingRunner{}
	newService := func() *Service {
		return &Service{State: NewStateStore(dataDir), Dependencies: Dependencies{Discovery: Discovery{Runner: runner}}}
	}
	coordinator, err := SharedCoordinator(dataDir, newService(), store)
	if err != nil {
		t.Fatal(err)
	}

	request := DeployRequest{
		PeerIdentity: "peer-key", DisplayName: "stage6-test", CoreURL: "https://core.example",
		HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", ConfirmHostKey: true,
		Credentials: SSHCredentials{User: "root", Password: "one-time-password"},
	}
	const iterations = 80
	start := make(chan struct{})
	var wait sync.WaitGroup
	errCh := make(chan error, iterations*2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < iterations; index++ {
			if _, err := SharedCoordinator(dataDir, newService(), store); err != nil {
				errCh <- fmt.Errorf("refresh shared Coordinator: %w", err)
			}
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < iterations; index++ {
			key := fmt.Sprintf("preflight-race-key-%d", index)
			if _, err := coordinator.Start(ctx, request, key, sql.NullInt64{}, "127.0.0.1"); !errors.Is(err, ErrChangedHostKey) {
				errCh <- fmt.Errorf("Start must pass peer discovery and reject the changed host key, got %v", err)
			}
		}
	}()
	close(start)
	wait.Wait()
	close(errCh)
	for concurrentErr := range errCh {
		t.Error(concurrentErr)
	}
	if calls := runner.calls.Load(); calls < iterations {
		t.Fatalf("expected each Start to enter peer discovery before host-key rejection, got %d discovery calls for %d starts", calls, iterations)
	}
}

func TestInspectRemoteRejectsForeignSystemdUnitBeforeInstall(t *testing.T) {
	output := "Linux\nx86_64\n0\nnew\n-\nroot\nunit|foreign|-\ndocker|absent\n"
	remote := &stage6Remote{preflight: output}
	if _, err := InspectRemote(context.Background(), remote); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected foreign systemd unit refusal, got %v", err)
	}
	if len(remote.installs) != 0 {
		t.Fatalf("foreign systemd unit preflight must not start installation, got %d calls", len(remote.installs))
	}
}

func TestInspectRemoteRequiresProofOfAbsentSystemdUnit(t *testing.T) {
	remote := &stage6Remote{preflight: "Linux\nx86_64\n0\nnew\n-\nroot\nunit|absent|-\ndocker|absent\n"}
	if _, err := InspectRemote(context.Background(), remote); err != nil {
		t.Fatalf("valid absent-unit preflight output should parse: %v", err)
	}
	if len(remote.commands) != 1 {
		t.Fatalf("expected exactly one remote preflight, got %d", len(remote.commands))
	}
	command := remote.commands[0]
	for _, expected := range []string{
		"unit_load_state=\"$(systemctl show --property=LoadState --value nodedance-agent.service)\"",
		"cannot prove that the effective Agent systemd unit is absent",
		"/usr/lib/systemd/system/nodedance-agent.service",
		"grep -Fxq 'User=root' \"$unit_path\"",
		"grep -Fxq 'Group=root' \"$unit_path\"",
	} {
		if !strings.Contains(command, expected) {
			t.Errorf("remote preflight is missing fail-closed unit check %q", expected)
		}
	}
	if strings.Contains(command, "FragmentPath --value nodedance-agent.service 2>/dev/null || true") {
		t.Fatal("remote preflight must not convert a FragmentPath query error to an absent unit")
	}
	for _, retired := range []string{"NoNewPrivileges=true", "ProtectSystem=strict", "ProtectHome=tmpfs", "User=[a-zA-Z0-9_.-]+"} {
		if strings.Contains(command, retired) {
			t.Fatalf("remote preflight still recognizes a restricted service unit through %q", retired)
		}
	}
}

func TestManualAgentInstallUIUsesRootOnlyCommands(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
	uiPath := filepath.Join(repoRoot, "web", "src", "components", "TailscaleDiscovery.vue")
	ui, err := os.ReadFile(uiPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ui), `<AgentEnrollment compact />`) {
		t.Fatal("Tailscale discovery page does not expose the shared Agent enrollment flow")
	}
	enrollmentUI, err := os.ReadFile(filepath.Join(repoRoot, "web", "src", "components", "AgentEnrollment.vue"))
	if err != nil {
		t.Fatal(err)
	}
	installer, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "install-agent.sh"))
	if err != nil {
		t.Fatal(err)
	}
	releaseWorkflow, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "agent-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"bash -o pipefail -c",
		"--proto =https --proto-redir =https --tlsv1.2",
		"api.github.com/repos/CST-Cat/NodeDance/releases?per_page=100",
		"agent-v[0-9]+",
		"releases/download/$tag/install-agent.sh",
		"sudo bash -s --",
		"data-testid=\"agent-install-command\"",
		"data-testid=\"agent-enrollment-token\"",
		"隐藏提示输入此 Token",
	} {
		if !strings.Contains(string(enrollmentUI), required) {
			t.Errorf("shared Agent enrollment UI is missing root-only behavior %q", required)
		}
	}
	for _, required := range []string{
		"--release-tag TAG",
		"releases/download\"",
		"${RELEASE_BASE}/${release_tag}/SHA256SUMS",
		"${RELEASE_BASE}/${release_tag}/${asset}",
		"--token-stdin",
		"prepare-systemd-state --require-new",
		"install-systemd --config \"$CONFIG_PATH\" --enable",
	} {
		if !strings.Contains(string(installer), required) {
			t.Errorf("Agent installer is missing root-only behavior %q", required)
		}
	}
	if strings.Contains(string(enrollmentUI)+string(installer), "releases/latest/download") {
		t.Fatal("Agent enrollment must not use the repository-wide latest release URL")
	}
	if !strings.Contains(string(releaseWorkflow), "- 'agent-v*'") || strings.Contains(string(releaseWorkflow), "- 'v*'") {
		t.Fatal("Agent release workflow must be triggered only by Agent-prefixed tags")
	}
	for _, retired := range []string{
		"sudo -u",
		"sudo -u nodedance-agent",
		"useradd --system",
		"install-systemd --user nodedance-agent",
		"nodedance-agent:nodedance-agent",
	} {
		if strings.Contains(string(enrollmentUI)+string(installer), retired) {
			t.Errorf("Agent enrollment still emits restricted service behavior %q", retired)
		}
	}
}

type stage6LocalShellRemote struct{ path string }

func (r stage6LocalShellRemote) Run(ctx context.Context, _ string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, "/bin/sh", "-s")
	command.Stdin = strings.NewReader(string(input))
	command.Env = append(os.Environ(), "PATH="+r.path+":"+os.Getenv("PATH"))
	return command.CombinedOutput()
}

func (stage6LocalShellRemote) Close() error { return nil }

func TestInspectRemoteFailsClosedWhenFragmentQueryFails(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "systemctl.log")
	commands := map[string]string{
		"systemctl": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\nexit 29\n",
		"id":        "#!/bin/sh\n[ \"$1\" = -u ] && { echo 0; exit 0; }\nexit 1\n",
		"uname":     "#!/bin/sh\n[ \"$1\" = -s ] && { echo Linux; exit 0; }\n[ \"$1\" = -m ] && { echo x86_64; exit 0; }\nexit 1\n",
	}
	for name, script := range commands {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SYSTEMCTL_LOG", logPath)
	if _, err := InspectRemote(context.Background(), stage6LocalShellRemote{path: directory}); err == nil {
		t.Fatal("SSH preflight must fail when the effective FragmentPath query fails")
	}
	logged, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(logged), "show --property=FragmentPath --value nodedance-agent.service") {
		t.Fatalf("preflight did not stop at the failed effective-unit query: err=%v log=%q", err, logged)
	}
}

func TestRemoteInstallUnitQueryFailureAbortsBeforeWritesOrReload(t *testing.T) {
	remote := &stage6Remote{}
	preflight := Preflight{OS: "linux", Arch: "x86_64", UID: 0, UnitState: "absent"}
	artifact := Artifact{Bytes: []byte("stage6-local-artifact"), OS: "linux", Arch: "x86_64"}
	if err := InstallRemoteWithFileRootOptions(context.Background(), remote, preflight, artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, "", false); err != nil {
		t.Fatal(err)
	}
	if len(remote.installs) != 1 {
		t.Fatalf("expected generated install script, got %d", len(remote.installs))
	}
	script := remote.installs[0]
	guardStart := strings.Index(script, "EXPECTED_UNIT_STATE='absent'")
	firstSnapshot := strings.Index(script, "if [ -x /usr/local/bin/nodedance-agent ]")
	if guardStart < 0 || firstSnapshot < 0 || guardStart > firstSnapshot {
		t.Fatal("effective-unit guard must run before snapshot or installation writes")
	}
	guardEnd := strings.Index(script[guardStart:], "\nif [ -x /usr/local/bin/nodedance-agent ]")
	if guardEnd < 0 {
		t.Fatal("could not isolate the initial effective-unit guard")
	}
	guard := script[guardStart : guardStart+guardEnd]
	directory := t.TempDir()
	logPath := filepath.Join(directory, "systemctl.log")
	systemctl := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\nexit 29\n"
	if err := os.WriteFile(filepath.Join(directory, "systemctl"), []byte(systemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	testScript := "set -eu\n" + guard + "\necho guard-passed\n"
	command := exec.Command("/bin/sh", "-c", testScript)
	command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "SYSTEMCTL_LOG="+logPath)
	output, runErr := command.CombinedOutput()
	if runErr == nil || strings.Contains(string(output), "guard-passed") {
		t.Fatalf("installer guard must stop on a failed effective-unit query: err=%v output=%s", runErr, output)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil || strings.TrimSpace(string(logged)) != "show --property=FragmentPath --value nodedance-agent.service" {
		t.Fatalf("failed install guard must not daemon-reload: err=%v log=%q", err, logged)
	}
}

func TestInstallRemotePassesSystemdPreflightFingerprintToInstaller(t *testing.T) {
	for _, state := range []struct {
		name string
		unit string
		sum  string
	}{
		{name: "absent", unit: "absent"},
		{name: "managed", unit: "managed", sum: strings.Repeat("a", 64)},
	} {
		t.Run(state.name, func(t *testing.T) {
			remote := &stage6Remote{}
			preflight := Preflight{OS: "linux", Arch: "x86_64", UID: 0, UnitState: state.unit, UnitSHA256: state.sum}
			artifact := Artifact{Bytes: []byte("stage6-local-artifact"), OS: "linux", Arch: "x86_64"}
			err := InstallRemoteWithFileRootOptions(context.Background(), remote, preflight, artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, "", false)
			if err != nil {
				t.Fatalf("build deployment script: %v", err)
			}
			if len(remote.installs) != 1 {
				t.Fatalf("expected one generated install script, got %d", len(remote.installs))
			}
			script := remote.installs[0]
			wantState := "export NODEDANCE_INTERNAL_EXPECTED_SYSTEMD_UNIT_STATE='" + state.unit + "'"
			wantSum := "export NODEDANCE_INTERNAL_EXPECTED_SYSTEMD_UNIT_SHA256='" + state.sum + "'"
			for _, expected := range []string{
				wantState,
				wantSum,
				"export NODEDANCE_INTERNAL_SYSTEMD_RESULT_FILE=\"$TMP/installed-unit.sha256\"",
				"INSTALLED_UNIT_SHA256=\"$installed_unit_hash\"",
				"current_unit_hash\" != \"$INSTALLED_UNIT_SHA256\"",
				"systemd unit changed concurrently; preserving it for manual review",
				"systemd unit changed before rollback; preserving it for manual review",
				"unit_load_state=\"$(systemctl show --property=LoadState --value nodedance-agent.service)\"",
				"unit_shadow=0",
				"[ \"$unit_load_state\" = not-found ]",
			} {
				if !strings.Contains(script, expected) {
					t.Errorf("generated deployment script is missing %q", expected)
				}
			}
			if strings.Contains(script, "FragmentPath --value nodedance-agent.service 2>/dev/null || true") {
				t.Fatal("installer must not treat an effective-unit query failure as absence")
			}
			syntax := exec.Command("/bin/sh", "-n")
			syntax.Stdin = strings.NewReader(script)
			if output, err := syntax.CombinedOutput(); err != nil {
				t.Fatalf("generated deployment script has invalid shell syntax: %v: %s", err, output)
			}
		})
	}
}

func TestGeneratedInstallValidatesAgentStateBeforeAnySystemWrites(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("generated SSH installer targets Linux")
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		t.Skipf("unsupported test build architecture %q", runtime.GOARCH)
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
	agentBinary := filepath.Join(t.TempDir(), "nodedance-agent")
	build := exec.Command("go", "build", "-o", agentBinary, "./cmd/nodedance-agent")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the Agent artifact for the generated install script: %v: %s", err, output)
	}
	artifactBytes, err := os.ReadFile(agentBinary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifactBytes)
	remote := &stage6Remote{}
	preflight := Preflight{OS: "linux", Arch: arch, UID: 0, UnitState: "absent"}
	artifact := Artifact{Bytes: artifactBytes, OS: "linux", Arch: arch, SHA256: hex.EncodeToString(digest[:])}
	if err := InstallRemoteWithFileRootOptions(context.Background(), remote, preflight, artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, "", false); err != nil {
		t.Fatalf("generate Agent installation script: %v", err)
	}
	if len(remote.installs) != 1 {
		t.Fatalf("expected one generated install script, got %d", len(remote.installs))
	}
	script := remote.installs[0]
	validateAt := strings.Index(script, `"$TMP/nodedance-agent" validate-systemd-state --config /var/lib/nodedance-agent/agent.json`)
	prepareAt := strings.Index(script, "state_config=\"$(\"$TMP/nodedance-agent\" prepare-systemd-state --user root)\"")
	snapshotAt := strings.Index(script, "if [ -x /usr/local/bin/nodedance-agent ]")
	installAt := strings.Index(script, "install -m 0755 \"$TMP/nodedance-agent\" /usr/local/bin/.nodedance-agent.new")
	if validateAt < 0 || prepareAt < 0 || snapshotAt < 0 || installAt < 0 || validateAt > prepareAt || prepareAt > snapshotAt || validateAt > installAt {
		t.Fatal("generated installer must validate state paths before root preparation, snapshots, or binary installation")
	}
	for _, forbidden := range []string{
		"useradd --system",
		"getent passwd nodedance-agent",
		"runuser -u nodedance-agent",
		"prepare-systemd-state --user nodedance-agent",
		"install-systemd --user nodedance-agent",
		"refusing to run Agent as root service account",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("generated installer retained restricted service behavior %q", forbidden)
		}
	}
	for _, required := range []string{
		"prepare-systemd-state --user root",
		"install-systemd --user root",
		"User=root",
		"Group=root",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("generated installer is missing root-only service behavior %q", required)
		}
	}

	for _, testCase := range []struct {
		name      string
		setup     func(*testing.T, string) []string
		wantError bool
	}{
		{
			name: "state directory symlink",
			setup: func(t *testing.T, root string) []string {
				t.Helper()
				victim := filepath.Join(root, "victim-state")
				if err := os.Mkdir(victim, 0o751); err != nil {
					t.Fatal(err)
				}
				victimConfig := filepath.Join(victim, "agent.json")
				if err := os.WriteFile(victimConfig, []byte("unrelated-state\n"), 0o640); err != nil {
					t.Fatal(err)
				}
				stateDir := filepath.Join(root, "var", "lib", "nodedance-agent")
				if err := os.Symlink(victim, stateDir); err != nil {
					t.Fatal(err)
				}
				return []string{victim, victimConfig}
			},
			wantError: true,
		},
		{
			name: "config symlink",
			setup: func(t *testing.T, root string) []string {
				t.Helper()
				stateDir := filepath.Join(root, "var", "lib", "nodedance-agent")
				if err := os.Mkdir(stateDir, 0o755); err != nil {
					t.Fatal(err)
				}
				victim := filepath.Join(root, "unrelated.json")
				if err := os.WriteFile(victim, []byte("unrelated-config\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(stateDir, "agent.json")); err != nil {
					t.Fatal(err)
				}
				return []string{stateDir, victim}
			},
			wantError: true,
		},
		{
			name: "new unlinked state path",
			setup: func(*testing.T, string) []string {
				return nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			for _, directory := range []string{"tmp", "var/lib", "usr/local/bin", "etc/systemd/system", "run/systemd/system", "usr/local/lib/systemd/system", "usr/lib/systemd/system", "lib/systemd/system", "bin"} {
				if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			victims := testCase.setup(t, root)
			before := make([]systemPathSnapshot, 0, len(victims))
			for _, victim := range victims {
				before = append(before, snapshotSystemPath(t, victim))
			}
			fakeSystemctl := "#!/bin/sh\ncase \"$1:$2\" in\nshow:--property=FragmentPath) printf '\\n' ;;\nshow:--property=LoadState) printf 'not-found\\n' ;;\n*) exit 3 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(root, "bin", "systemctl"), []byte(fakeSystemctl), 0o755); err != nil {
				t.Fatal(err)
			}

			stagedScript := script
			for _, path := range []string{
				"/var/lib/nodedance-agent",
				"/usr/local/bin",
				"/etc/systemd/system",
				"/run/systemd/system",
				"/usr/local/lib/systemd/system",
				"/usr/lib/systemd/system",
				"/lib/systemd/system",
				"/var/run/docker.sock",
				"/tmp/nodedance-deploy.",
			} {
				stagedScript = strings.ReplaceAll(stagedScript, path, filepath.Join(root, strings.TrimPrefix(path, "/")))
			}
			prefixEnd := strings.Index(stagedScript, "\nstate_config=\"$(\"$TMP/nodedance-agent\" prepare-systemd-state --user root)\"")
			if prefixEnd < 0 {
				t.Fatal("cannot isolate generated installer preflight")
			}
			command := exec.Command("/bin/sh", "-s")
			command.Stdin = strings.NewReader(stagedScript[:prefixEnd])
			command.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
			output, runErr := command.CombinedOutput()
			if testCase.wantError && runErr == nil {
				t.Fatalf("generated installer accepted a symlinked state path: %s", output)
			}
			if !testCase.wantError && runErr != nil {
				t.Fatalf("generated installer rejected a safe new state path: %v: %s", runErr, output)
			}
			for index, victim := range victims {
				assertSystemPathUnchanged(t, victim, before[index])
			}
			if _, err := os.Lstat(filepath.Join(root, "usr", "local", "bin", ".nodedance-agent.new")); !os.IsNotExist(err) {
				t.Fatalf("installer wrote a global binary before path validation: %v", err)
			}
		})
	}
}

type systemPathSnapshot struct {
	mode os.FileMode
	uid  uint32
	gid  uint32
	data string
}

func snapshotSystemPath(t *testing.T, path string) systemPathSnapshot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type for %s", path)
	}
	snapshot := systemPathSnapshot{mode: info.Mode(), uid: stat.Uid, gid: stat.Gid}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.data = string(data)
	}
	return snapshot
}

func assertSystemPathUnchanged(t *testing.T, path string, want systemPathSnapshot) {
	t.Helper()
	if got := snapshotSystemPath(t, path); got != want {
		t.Fatalf("target changed at %s: got=%+v want=%+v", path, got, want)
	}
}

func TestNodeDanceUnitRollbackPreservesConcurrentChanges(t *testing.T) {
	managedOld := "# NodeDanceAgentUnit=1\n[Unit]\nDescription=NodeDance Agent\nType=simple\n"
	managedInstalled := "# NodeDanceAgentUnit=1\n[Unit]\nDescription=NodeDance Agent\nType=simple\nExecStart=new\n"
	managedConcurrent := "# NodeDanceAgentUnit=1\n[Unit]\nDescription=NodeDance Agent\nType=simple\nExecStart=concurrent\n"
	foreign := "[Unit]\nDescription=Foreign service\n"
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	run := func(t *testing.T, expectedState, expectedHash, installedHash, current, previous string, hadUnit bool) (string, string, bool) {
		t.Helper()
		root := t.TempDir()
		unitPath := filepath.Join(root, "nodedance-agent.service")
		previousPath := filepath.Join(root, "old-unit")
		if current != "" {
			if err := os.WriteFile(unitPath, []byte(current), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if hadUnit {
			if err := os.WriteFile(previousPath, []byte(previous), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		had := "0"
		if hadUnit {
			had = "1"
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		script := fmt.Sprintf(`set -eu
TMP=%s
unit_path=%s
EXPECTED_UNIT_STATE=%s
EXPECTED_UNIT_SHA256=%s
INSTALLED_UNIT_SHA256=%s
UNIT_MAY_HAVE_CHANGED=1
UNIT_CHANGED=0
HAD_UNIT=%s
is_nodedance_unit() { [ -f "$1" ] && [ ! -L "$1" ] && grep -Fxq '# NodeDanceAgentUnit=1' "$1" && grep -Fxq '[Unit]' "$1" && grep -Fxq 'Description=NodeDance Agent' "$1" && grep -Fxq 'Type=simple' "$1"; }
%s
rollback_unit
printf 'RESULT:%%s\n' "$UNIT_CHANGED"
`, quote(root), quote(unitPath), quote(expectedState), quote(expectedHash), quote(installedHash), had, nodeDanceUnitRollbackScript())
		command := exec.Command("/bin/sh", "-c", script)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("execute isolated unit rollback: %v: %s", err, output)
		}
		marker := strings.LastIndex(string(output), "RESULT:")
		if marker < 0 {
			t.Fatalf("rollback helper did not report a result: %s", output)
		}
		changed := strings.TrimSpace(string(output)[marker+len("RESULT:"):])
		currentAfter, err := os.ReadFile(unitPath)
		if os.IsNotExist(err) {
			return changed, "", true
		}
		if err != nil {
			t.Fatal(err)
		}
		return changed, string(currentAfter), false
	}

	t.Run("absent preflight never deletes concurrent foreign unit", func(t *testing.T) {
		changed, current, absent := run(t, "absent", "", digest(managedInstalled), foreign, "", false)
		if absent || current != foreign || changed != "0" {
			t.Fatalf("concurrent foreign unit was not preserved: absent=%v changed=%q current=%q", absent, changed, current)
		}
	})
	t.Run("managed preflight preserves changed marked unit", func(t *testing.T) {
		changed, current, absent := run(t, "managed", digest(managedOld), digest(managedInstalled), managedConcurrent, managedOld, true)
		if absent || current != managedConcurrent || changed != "0" {
			t.Fatalf("concurrent NodeDance unit was not preserved: absent=%v changed=%q current=%q", absent, changed, current)
		}
	})
	t.Run("managed install restores only exact installed unit", func(t *testing.T) {
		changed, current, absent := run(t, "managed", digest(managedOld), digest(managedInstalled), managedInstalled, managedOld, true)
		if absent || current != managedOld || changed != "1" {
			t.Fatalf("expected atomic restore of the previous managed unit: absent=%v changed=%q current=%q", absent, changed, current)
		}
	})
	t.Run("absent install removes only exact installed unit", func(t *testing.T) {
		changed, current, absent := run(t, "absent", "", digest(managedInstalled), managedInstalled, "", false)
		if !absent || current != "" || changed != "1" {
			t.Fatalf("expected exact newly installed unit removal: absent=%v changed=%q current=%q", absent, changed, current)
		}
	})
}

func TestInstallRollbackBeforeSnapshotPreservesExistingAgentArtifacts(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "nodedance-agent")
	binDir := filepath.Join(root, "bin")
	if err := os.WriteFile(binary, []byte("original-agent-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "helper"), []byte("original-helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := fmt.Sprintf(`set -eu
TMP=%s
agent_binary_path=%s
agent_bin_path=%s
SNAPSHOT_COMPLETE=0
HAD_BINARY=0
HAD_AGENT_BIN=0
%s
rollback_agent_artifacts
`, quote(root), quote(binary), quote(binDir), nodeDanceArtifactRollbackScript())
	command := exec.Command("/bin/sh", "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute pre-snapshot rollback guard: %v: %s", err, output)
	}
	gotBinary, err := os.ReadFile(binary)
	if err != nil || string(gotBinary) != "original-agent-binary" {
		t.Fatalf("pre-snapshot rollback changed existing binary: err=%v contents=%q", err, gotBinary)
	}
	gotHelper, err := os.ReadFile(filepath.Join(binDir, "helper"))
	if err != nil || string(gotHelper) != "original-helper" {
		t.Fatalf("pre-snapshot rollback changed existing Agent bin directory: err=%v contents=%q", err, gotHelper)
	}
}
