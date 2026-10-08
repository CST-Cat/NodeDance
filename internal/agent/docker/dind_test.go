package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDINDEventLifetimeAndReconnectSnapshot is intentionally opt-in. The
// script scripts/test/s04-docker-dind.sh supplies a repository-owned DIND
// socket and unique run labels; this test never uses the host Docker socket.
func TestDINDEventLifetimeAndReconnectSnapshot(t *testing.T) {
	endpoint := os.Getenv("NODEDANCE_S04_DIND_HOST")
	if endpoint == "" {
		t.Skip("set NODEDANCE_S04_DIND_HOST via scripts/test/s04-docker-dind.sh")
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatalf("test requires an owned Unix socket, got %q", endpoint)
	}
	socket := strings.TrimPrefix(endpoint, "unix://")
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dindRoot := s04DINDRoot(t, repoRoot)
	serverVersion := verifyOwnedDIND(t, dindRoot, socket)
	t.Logf("owned DIND server=%s socket=%s", serverVersion, socket)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	image := lockedBusyboxImage(t, repoRoot)
	if _, err := dockerCLI(ctx, endpoint, "image", "inspect", image); err != nil {
		if _, pullErr := dockerCLI(ctx, endpoint, "pull", image); pullErr != nil {
			t.Fatalf("pull locked BusyBox image: %v", pullErr)
		}
	}

	suite := fmt.Sprintf("nodedance-s04-%d", time.Now().UnixNano())
	name := "nd-s04-" + strings.TrimPrefix(suite, "nodedance-s04-")
	id, err := dockerCLI(ctx, endpoint,
		"run", "--detach", "--name", name,
		"--label", "io.nodedance.test=true",
		"--label", "io.nodedance.suite="+suite,
		image, "sh", "-c", "while true; do sleep 0.1; done",
	)
	if err != nil {
		t.Fatalf("create owned event-stream fixture: %v", err)
	}
	id = strings.TrimSpace(id)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		label, inspectErr := dockerCLI(cleanupCtx, endpoint, "inspect", "--format", "{{ index .Config.Labels \"io.nodedance.suite\" }}", id)
		if inspectErr != nil || strings.TrimSpace(label) != suite {
			t.Errorf("refusing to remove fixture with mismatched owner label: label=%q err=%v", label, inspectErr)
			return
		}
		if _, removeErr := dockerCLI(cleanupCtx, endpoint, "rm", "--force", id); removeErr != nil {
			t.Errorf("remove owned fixture %s: %v", id, removeErr)
		}
	})

	sdk, err := NewSDKEngine(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	control := &firstEventStreamControl{first: make(chan func(), 1)}
	engine := &controlledEventsEngine{Engine: sdk, control: control}
	options := Options{
		FullScanInterval: time.Hour,
		HealthInterval:   100 * time.Millisecond,
		ReconnectMin:     3 * time.Second,
		ReconnectMax:     3 * time.Second,
	}
	discoverer, err := NewDiscoverer(engine, nil, options)
	if err != nil {
		_ = sdk.Close()
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	runStopped := false
	go func() { runDone <- discoverer.Run(runCtx) }()
	t.Cleanup(func() {
		stopRun()
		if runStopped {
			return
		}
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("Run returned an error during cleanup: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not finish during event test cleanup")
		}
	})

	waitDIND(t, discoverer, id, func(item Container, health Health) bool {
		return item.Running && health.EventsConnected && health.SnapshotFresh
	}, 10*time.Second, "initial full snapshot and live event stream")
	var closeFirstStream func()
	select {
	case closeFirstStream = <-control.first:
	case <-ctx.Done():
		t.Fatal("first SDK event stream was not opened")
	}

	// The SDK Events request context remains attached to the streaming response.
	// Waiting beyond RequestTimeout proves it was not canceled after handshake.
	time.Sleep(DefaultRequestTimeout + 500*time.Millisecond)
	_, health := discoverer.Current()
	if !health.EventsConnected {
		t.Fatalf("SDK event stream did not survive RequestTimeout: %+v", health)
	}
	started := time.Now()
	if _, err := dockerCLI(ctx, endpoint, "stop", "--time", "1", id); err != nil {
		t.Fatalf("stop fixture through real Engine: %v", err)
	}
	waitDIND(t, discoverer, id, func(item Container, _ Health) bool { return !item.Running && item.State == "exited" }, 5*time.Second, "event-driven stop reconciliation")
	convergence := time.Since(started)
	if convergence > 5*time.Second {
		t.Fatalf("real Docker stop event exceeded the 5s convergence budget: %s", convergence)
	}
	t.Logf("real Docker stop event reconciled in %s after the stream lived beyond RequestTimeout", convergence)
	if _, err := dockerCLI(ctx, endpoint, "start", id); err != nil {
		t.Fatalf("start fixture through real Engine: %v", err)
	}
	waitDIND(t, discoverer, id, func(item Container, _ Health) bool { return item.Running && item.State == "running" }, 5*time.Second, "event-driven start reconciliation")

	// Close only the real Engine event request while leaving the daemon active.
	// A state change during the reconnect backoff is therefore genuinely missed
	// by events and must be repaired by the reconnect-triggered full snapshot.
	closeFirstStream()
	waitDINDHealth(t, discoverer, func(health Health) bool { return !health.EventsConnected }, 3*time.Second, "event stream interruption")
	if _, err := dockerCLI(ctx, endpoint, "stop", "--time", "1", id); err != nil {
		t.Fatalf("stop fixture during event gap: %v", err)
	}
	waitDINDHealth(t, discoverer, func(health Health) bool { return health.EventsConnected }, 8*time.Second, "event stream reconnect")
	waitDIND(t, discoverer, id, func(item Container, health Health) bool {
		return !item.Running && item.State == "exited" && health.SnapshotFresh
	}, 8*time.Second, "reconnect full-snapshot convergence after a missed event")
	t.Log("event gap recovered from a complete post-reconnect snapshot; Docker event history replay was not assumed")

	engineNumber := strings.SplitN(serverVersion, ".", 2)[0]
	engineStopped := false
	t.Cleanup(func() {
		if engineStopped {
			restartCtx, restartCancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer restartCancel()
			if output, restartErr := runDINDHarness(restartCtx, dindRoot, "start", engineNumber); restartErr != nil {
				t.Errorf("restore owned DIND after failed outage test: %v\n%s", restartErr, output)
			}
		}
	})
	if output, err := runDINDHarness(ctx, dindRoot, "stop", engineNumber); err != nil {
		t.Fatalf("stop owned DIND for Engine outage test: %v\n%s", err, output)
	}
	engineStopped = true
	waitDIND(t, discoverer, id, func(item Container, health Health) bool {
		return item.Stale && item.UnavailableReason != "" && item.State == "exited" && health.Availability == EngineUnavailable
	}, 10*time.Second, "offline Engine retains and marks the last-known container stale")
	if output, err := runDINDHarness(ctx, dindRoot, "start", engineNumber); err != nil {
		t.Fatalf("restart owned DIND after Engine outage: %v\n%s", err, output)
	}
	engineStopped = false
	waitDIND(t, discoverer, id, func(item Container, health Health) bool {
		return !item.Stale && item.UnavailableReason == "" && item.State == "exited" && health.Availability == EngineAvailable && health.SnapshotFresh && health.EventsConnected
	}, 15*time.Second, "Engine restart refreshes and clears stale state")
	t.Log("DIND Engine outage retained the last known asset as stale; restart reconciled it with a new full snapshot")

	stopRun()
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned an error on clean cancellation: %v", runErr)
		}
		runStopped = true
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not cancel and join the SDK event/scan/health workers")
	}
	probe, err := NewSDKEngine(endpoint)
	if err != nil {
		t.Fatalf("create post-run SDK client: %v", err)
	}
	if err := probe.Ping(ctx); err != nil {
		_ = probe.Close()
		t.Fatalf("Engine connection was not usable after Run joined and closed its SDK client: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Errorf("close post-run SDK client: %v", err)
	}
}

// TestDINDInventoryMatchesOwnedFixtures compares the Discoverer cache with
// containers created outside NodeDance by the repository fixture script.
func TestDINDInventoryMatchesOwnedFixtures(t *testing.T) {
	endpoint := os.Getenv("NODEDANCE_S04_DIND_HOST")
	runID := os.Getenv("NODEDANCE_S04_FIXTURE_RUN_ID")
	if endpoint == "" || runID == "" {
		t.Skip("set the owned DIND host and fixture run ID via the S04 fixture harness")
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatalf("test requires an owned Unix socket, got %q", endpoint)
	}
	if len(runID) > 32 || strings.ContainsAny(runID, " /\\") {
		t.Fatalf("invalid owned fixture run ID %q", runID)
	}
	socket := strings.TrimPrefix(endpoint, "unix://")
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	version := verifyOwnedDIND(t, s04DINDRoot(t, repoRoot), socket)
	t.Logf("owned DIND server=%s fixture=%s", version, runID)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	image := lockedBusyboxImage(t, repoRoot)
	suiteLabel := "io.nodedance.suite=" + runID
	ownedName := func(prefix string) string { return prefix + "-" + runID }
	create := func(name string, flags ...string) string {
		t.Helper()
		args := []string{"create", "--name", name, "--label", "io.nodedance.test=true", "--label", suiteLabel}
		args = append(args, flags...)
		args = append(args, image, "sh", "-c", "while true; do sleep 3600; done")
		id, createErr := dockerCLI(ctx, endpoint, args...)
		if createErr != nil {
			t.Fatalf("create S04 inventory fixture %s: %v", name, createErr)
		}
		id = strings.TrimSpace(id)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			label, inspectErr := dockerCLI(cleanupCtx, endpoint, "inspect", "--format", "{{ index .Config.Labels \"io.nodedance.suite\" }}", id)
			if inspectErr != nil || strings.TrimSpace(label) != runID {
				t.Errorf("refusing to remove fixture with mismatched owner label: name=%s label=%q err=%v", name, label, inspectErr)
				return
			}
			if _, removeErr := dockerCLI(cleanupCtx, endpoint, "rm", "--force", id); removeErr != nil {
				t.Errorf("remove owned S04 fixture %s: %v", name, removeErr)
			}
		})
		return id
	}
	start := func(id string) {
		t.Helper()
		if _, startErr := dockerCLI(ctx, endpoint, "start", id); startErr != nil {
			t.Fatalf("start S04 inventory fixture %s: %v", id, startErr)
		}
	}

	createdName := ownedName("nd-s04-created")
	createdID := create(createdName, "--expose", "65000/tcp")
	dynamicID := create(ownedName("nd-s04-dynamic"), "--publish-all", "--expose", "8080/tcp", "--expose", "5353/udp")
	ipv6ID := create(ownedName("nd-s04-ipv6"), "--publish", "[::1]:0:80/tcp", "--publish", "127.0.0.1:0:53/udp")
	startingID := create(ownedName("nd-s04-starting"), "--health-cmd", "true", "--health-interval", "1h", "--health-start-period", "2m")
	start(dynamicID)
	start(ipv6ID)
	start(startingID)

	sdk, err := NewSDKEngine(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	discoverer, err := NewDiscoverer(sdk, nil, Options{HealthInterval: 200 * time.Millisecond})
	if err != nil {
		_ = sdk.Close()
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- discoverer.Run(runCtx) }()
	t.Cleanup(func() {
		stopRun()
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("Run returned an error during cleanup: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not finish during inventory test cleanup")
		}
	})

	expectedNames := []string{
		"nd-web-" + runID, "nd-udp-" + runID, "nd-expose-" + runID,
		"nd-host-" + runID, "nd-stopped-" + runID, "nd-health-" + runID,
		"nd-log-" + runID,
		"nodedance-demo-" + runID + "-web-1",
		"nodedance-demo-" + runID + "-data-1",
		"nodedance-demo-" + runID + "-optional-worker-1",
		createdName, ownedName("nd-s04-dynamic"), ownedName("nd-s04-ipv6"), ownedName("nd-s04-starting"),
	}
	waitForDINDInventory(t, discoverer, expectedNames, 15*time.Second)
	items, _ := discoverer.Current()
	byName := make(map[string]Container, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}

	created := byName[createdName]
	if created.ID != createdID || created.State != "created" || created.Running || created.StartedAt != nil || created.FinishedAt != nil || created.CreatedAt == nil {
		t.Fatalf("never-started container state/time is not represented as unknown: %+v", created)
	}
	if !portExposedOnly(created, 65000, "tcp") {
		t.Fatalf("created-only EXPOSE became a host publication: %+v", created.Ports)
	}
	web := byName["nd-web-"+runID]
	if web.State != "running" || !hasHostBinding(web, 80, "tcp", "configured", "127.0.0.1", "18080") || !hasHostBinding(web, 80, "tcp", "published", "127.0.0.1", "18080") {
		t.Fatalf("real TCP configured/actual mapping differs from Engine inspect: %+v", web)
	}
	if len(web.Networks) == 0 || web.Networks[0].IPv4 == "" || !hasMountAt(byName["nodedance-demo-"+runID+"-web-1"], "/usr/share/nginx/html") {
		t.Fatalf("container network IP or Compose bind mount missing: web=%+v compose=%+v", web, byName["nodedance-demo-"+runID+"-web-1"])
	}
	udp := byName["nd-udp-"+runID]
	if !hasHostBinding(udp, 53, "udp", "configured", "127.0.0.1", "18053") || !hasHostBinding(udp, 53, "udp", "published", "127.0.0.1", "18053") {
		t.Fatalf("real UDP configured/actual mapping differs from Engine inspect: %+v", udp)
	}
	if !portExposedOnly(byName["nd-expose-"+runID], 2375, "tcp") {
		t.Fatalf("EXPOSE-only container has a fake host publication: %+v", byName["nd-expose-"+runID].Ports)
	}
	if !byName["nd-host-"+runID].HostNetwork || hasAnyPublishedPorts(byName["nd-host-"+runID]) {
		t.Fatalf("Host Network container reports a bridge publication: %+v", byName["nd-host-"+runID])
	}
	if hasAnyPublishedPorts(byName["nd-stopped-"+runID]) {
		t.Fatalf("stopped container reports an active host publication: %+v", byName["nd-stopped-"+runID].Ports)
	}
	if byName["nd-health-"+runID].Health != HealthHealthy || byName["nd-web-"+runID].Health != HealthNone || byName[ownedName("nd-s04-starting")].Health != HealthStarting {
		t.Fatalf("Docker health states were conflated: healthy=%s none=%s starting=%s", byName["nd-health-"+runID].Health, web.Health, byName[ownedName("nd-s04-starting")].Health)
	}
	dynamic := byName[ownedName("nd-s04-dynamic")]
	if !dynamic.PublishAllPorts || !hasPublishedPort(dynamic, 8080, "tcp") || !hasPublishedPort(dynamic, 5353, "udp") {
		t.Fatalf("dynamic IPv4 publications were not reported from Engine-assigned ports: %+v", dynamic)
	}
	ipv6 := byName[ownedName("nd-s04-ipv6")]
	if !hasHostBinding(ipv6, 80, "tcp", "configured", "::1", "") || !hasPublishedAddress(ipv6, 80, "tcp", "::1") || !hasPublishedPort(ipv6, 53, "udp") {
		t.Fatalf("IPv6 or mixed TCP/UDP actual mapping differs from Engine inspect: %+v", ipv6.Ports)
	}
	composeWeb := byName["nodedance-demo-"+runID+"-web-1"]
	if composeWeb.Compose == nil || composeWeb.Compose.Project != "nodedance-demo-"+runID || composeWeb.Compose.Service != "web" || composeWeb.Compose.ContainerNumber != "1" || composeWeb.Compose.WorkingDir == "" || composeWeb.Compose.ConfigFiles == "" {
		t.Fatalf("selected Compose identity labels were not retained: %+v", composeWeb.Compose)
	}
	composeData := byName["nodedance-demo-"+runID+"-data-1"]
	if !hasMountAt(composeData, "/data") {
		t.Fatalf("Compose data volume mount missing: %+v", composeData.Mounts)
	}
	encoded, err := json.Marshal(composeWeb)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"NODEDANCE_TEST_RUN_ID", "ND_FIXTURE_VALUE", "com.docker.compose", "labels", "environment"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("normalized container leaked raw labels or environment key %q: %s", forbidden, encoded)
		}
	}

	healthName := "nd-health-" + runID
	if _, err := dockerCLI(ctx, endpoint, "exec", healthName, "touch", "/tmp/nodedance-unhealthy"); err != nil {
		t.Fatalf("trigger real unhealthy healthcheck: %v", err)
	}
	waitDIND(t, discoverer, byName[healthName].ID, func(item Container, _ Health) bool { return item.Health == HealthUnhealthy }, 8*time.Second, "real unhealthy healthcheck")
	if _, err := dockerCLI(ctx, endpoint, "exec", healthName, "rm", "-f", "/tmp/nodedance-unhealthy"); err != nil {
		t.Fatalf("recover real healthcheck: %v", err)
	}
	waitDIND(t, discoverer, byName[healthName].ID, func(item Container, _ Health) bool { return item.Health == HealthHealthy }, 8*time.Second, "real healthcheck recovery")
	t.Logf("verified %d externally-created containers, TCP/UDP/IPv4/IPv6/dynamic mappings, EXPOSE/Host Network/stopped distinctions, health transitions, mounts, selected Compose identity, and never-started timestamps", len(expectedNames))
}

// TestDIND100ExternalChangesConvergenceP95 measures this isolated Agent-module
// observer path only. It is not evidence for the complete Core/Agent/WebSocket
// S04-SUP-01 acceptance case.
func TestDIND100ExternalChangesConvergenceP95(t *testing.T) {
	endpoint := os.Getenv("NODEDANCE_S04_DIND_HOST")
	runID := os.Getenv("NODEDANCE_S04_P95_RUN_ID")
	wantedEngine := os.Getenv("NODEDANCE_S04_DIND_ENGINE")
	if endpoint == "" || runID == "" || wantedEngine == "" {
		t.Skip("set the owned DIND endpoint, engine, and unique run ID via the S04 P95 harness")
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		t.Fatalf("test requires an owned Unix socket, got %q", endpoint)
	}
	if len(runID) > 32 || !validSuiteRunID(runID) {
		t.Fatalf("invalid owned S04 P95 run ID %q", runID)
	}
	socket := strings.TrimPrefix(endpoint, "unix://")
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	serverVersion := verifyOwnedDIND(t, s04DINDRoot(t, repoRoot), socket)
	if !strings.HasPrefix(serverVersion, wantedEngine+".") {
		t.Fatalf("requested Engine %s but owned marker reports %s", wantedEngine, serverVersion)
	}
	t.Logf("module-only S04 P95 run=%s owned Engine=%s socket=%s", runID, serverVersion, socket)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	image := lockedBusyboxImage(t, repoRoot)
	if _, err := dockerCLI(ctx, endpoint, "image", "inspect", image); err != nil {
		if _, pullErr := dockerCLI(ctx, endpoint, "pull", image); pullErr != nil {
			t.Fatalf("pull locked BusyBox image into owned DIND: %v", pullErr)
		}
	}

	suite := runID
	name := "nd-s04-p95-" + runID
	id, err := dockerCLI(ctx, endpoint,
		"run", "--detach", "--name", name,
		"--label", "io.nodedance.test=true",
		"--label", "io.nodedance.suite="+suite,
		image, "sh", "-c", "while true; do sleep 3600; done",
	)
	if err != nil {
		t.Fatalf("create owned P95 fixture in dedicated DIND: %v", err)
	}
	id = strings.TrimSpace(id)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		label, inspectErr := dockerCLI(cleanupCtx, endpoint, "inspect", "--format", "{{ index .Config.Labels \"io.nodedance.suite\" }}", id)
		if inspectErr != nil {
			if strings.Contains(strings.ToLower(inspectErr.Error()), "no such object") {
				return
			}
			t.Errorf("refusing to remove fixture whose ownership cannot be verified: id=%s err=%v", id, inspectErr)
			return
		}
		if strings.TrimSpace(label) != suite {
			t.Errorf("refusing to remove fixture with mismatched owner label: id=%s label=%q", id, strings.TrimSpace(label))
			return
		}
		if _, removeErr := dockerCLI(cleanupCtx, endpoint, "rm", "--force", id); removeErr != nil {
			t.Errorf("remove exact owned P95 fixture %s: %v", id, removeErr)
		}
	})

	sdk, err := NewSDKEngine(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	eventTap := &measurementEventsEngine{Engine: sdk, observed: make(chan observedEngineEvent, 512)}
	observer := &measurementObserver{updates: make(chan observedDockerUpdate, 512)}
	discoverer, err := NewDiscoverer(eventTap, observer, Options{HealthInterval: 200 * time.Millisecond})
	if err != nil {
		_ = sdk.Close()
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- discoverer.Run(runCtx) }()
	runStopped := false
	t.Cleanup(func() {
		stopRun()
		if runStopped {
			return
		}
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("Run returned an error during P95 test cleanup: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not join during P95 test cleanup")
		}
	})
	waitDIND(t, discoverer, id, func(item Container, health Health) bool {
		return item.Running && !item.Paused && health.EventsConnected && health.SnapshotFresh
	}, 15*time.Second, "initial owned fixture snapshot and event stream")

	const changeCount = 100
	const convergenceBudget = 5 * time.Second
	engineEventLatencies := make([]time.Duration, 0, changeCount)
	operationLatencies := make([]time.Duration, 0, changeCount)
	for index := 0; index < changeCount; index++ {
		wantPaused := index%2 == 0
		action := "pause"
		if !wantPaused {
			action = "unpause"
		}
		operationStartedAt := time.Now().UTC()
		if _, err := dockerCLI(ctx, endpoint, action, id); err != nil {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=command_failed operation_started_at=%s error=%q", index+1, action, operationStartedAt.Format(time.RFC3339Nano), err.Error())
			t.Fatalf("owned DIND external change %d/%d (%s) failed: %v", index+1, changeCount, action, err)
		}
		commandCompletedAt := time.Now().UTC()
		pausedValue, inspectErr := dockerCLI(ctx, endpoint, "inspect", "--format", "{{.State.Paused}}", id)
		if inspectErr != nil || strings.TrimSpace(pausedValue) != fmt.Sprint(wantPaused) {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=engine_state_mismatch operation_started_at=%s command_completed_at=%s actual_paused=%q inspect_error=%q", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), strings.TrimSpace(pausedValue), errorText(inspectErr))
			t.Fatalf("owned Engine did not confirm requested pause state on change %d/%d", index+1, changeCount)
		}
		engineEvent, engineObservedAt, eventErr := waitForMeasurementEvent(eventTap.observed, id, action, convergenceBudget)
		if eventErr != nil {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=engine_event_missing operation_started_at=%s command_completed_at=%s timeout_ms=%d", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), convergenceBudget.Milliseconds())
			t.Fatalf("no matching real Docker Engine %q event for change %d/%d within %s: %v", action, index+1, changeCount, convergenceBudget, eventErr)
		}
		if engineEvent.event.EngineTime.IsZero() {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=engine_timestamp_missing operation_started_at=%s command_completed_at=%s event_received_at=%s", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), engineObservedAt.Format(time.RFC3339Nano))
			t.Fatalf("real Docker Engine event %q omitted its timestamp on change %d/%d", action, index+1, changeCount)
		}
		update, observerErr := waitForMeasurementUpdate(observer.updates, id, wantPaused, convergenceBudget)
		if observerErr != nil {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=observer_update_missing operation_started_at=%s command_completed_at=%s engine_event_at=%s engine_event_received_at=%s engine_event_wait_ms=%.3f timeout_ms=%d", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), engineEvent.event.EngineTime.Format(time.RFC3339Nano), engineObservedAt.Format(time.RFC3339Nano), engineObservedAt.Sub(operationStartedAt).Seconds()*1000, convergenceBudget.Milliseconds())
			t.Fatalf("NodeDance module observer did not report %s state for change %d/%d within %s: %v", action, index+1, changeCount, convergenceBudget, observerErr)
		}
		engineLatency := update.arrivedAt.Sub(engineEvent.event.EngineTime)
		operationLatency := update.arrivedAt.Sub(operationStartedAt)
		if engineLatency < 0 || operationLatency < 0 {
			t.Fatalf("clock order invalid for sample %d: event-to-observer=%s operation-to-observer=%s event=%s observer=%s", index+1, engineLatency, operationLatency, engineEvent.event.EngineTime, update.arrivedAt)
		}
		if engineLatency > convergenceBudget {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=over_budget operation_started_at=%s command_completed_at=%s engine_event_at=%s engine_event_received_at=%s inspect_observed_at=%s observer_arrived_at=%s engine_event_to_observer_ms=%.3f operation_to_observer_ms=%.3f budget_ms=%d", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), engineEvent.event.EngineTime.Format(time.RFC3339Nano), engineObservedAt.Format(time.RFC3339Nano), update.container.ObservedAt.Format(time.RFC3339Nano), update.arrivedAt.Format(time.RFC3339Nano), engineLatency.Seconds()*1000, operationLatency.Seconds()*1000, convergenceBudget.Milliseconds())
		} else {
			t.Logf("S04_MODULE_P95 sample=%03d action=%s outcome=observed operation_started_at=%s command_completed_at=%s engine_event_at=%s engine_event_received_at=%s inspect_observed_at=%s observer_arrived_at=%s engine_event_to_observer_ms=%.3f operation_to_observer_ms=%.3f", index+1, action, operationStartedAt.Format(time.RFC3339Nano), commandCompletedAt.Format(time.RFC3339Nano), engineEvent.event.EngineTime.Format(time.RFC3339Nano), engineObservedAt.Format(time.RFC3339Nano), update.container.ObservedAt.Format(time.RFC3339Nano), update.arrivedAt.Format(time.RFC3339Nano), engineLatency.Seconds()*1000, operationLatency.Seconds()*1000)
		}
		engineEventLatencies = append(engineEventLatencies, engineLatency)
		operationLatencies = append(operationLatencies, operationLatency)
	}

	eventP95 := durationNearestRank(engineEventLatencies, 0.95)
	operationP95 := durationNearestRank(operationLatencies, 0.95)
	t.Logf("S04_MODULE_P95_SUMMARY changes=%d observed=%d engine_event_to_observer_p50_ms=%.3f engine_event_to_observer_p95_ms=%.3f engine_event_to_observer_max_ms=%.3f operation_to_observer_p50_ms=%.3f operation_to_observer_p95_ms=%.3f operation_to_observer_max_ms=%.3f budget_ms=%d scope=agent-module-only full_s04_sup01=false", changeCount, len(engineEventLatencies), durationNearestRank(engineEventLatencies, 0.50).Seconds()*1000, eventP95.Seconds()*1000, maxDuration(engineEventLatencies).Seconds()*1000, durationNearestRank(operationLatencies, 0.50).Seconds()*1000, operationP95.Seconds()*1000, maxDuration(operationLatencies).Seconds()*1000, convergenceBudget.Milliseconds())
	if len(engineEventLatencies) != changeCount || eventP95 > convergenceBudget {
		t.Fatalf("module-only Engine event to observer P95 failed: observed=%d/%d p95=%s budget=%s", len(engineEventLatencies), changeCount, eventP95, convergenceBudget)
	}
	if operationP95 > convergenceBudget {
		t.Fatalf("module-only operation-start to observer P95 failed: p95=%s budget=%s", operationP95, convergenceBudget)
	}
	stopRun()
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("Run returned an error during clean P95 cancellation: %v", runErr)
		}
		runStopped = true
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not cancel and join after P95 measurement")
	}
}

type observedEngineEvent struct {
	event      Event
	receivedAt time.Time
}

type measurementEventsEngine struct {
	Engine
	observed chan observedEngineEvent
}

func (e *measurementEventsEngine) OpenEvents(ctx context.Context) (EventStream, error) {
	stream, err := e.Engine.OpenEvents(ctx)
	if err != nil {
		return EventStream{}, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	events := make(chan Event, 32)
	errorsOut := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		defer close(errorsOut)
		messages, streamErrors := stream.Events, stream.Errors
		for messages != nil || streamErrors != nil {
			select {
			case <-streamCtx.Done():
				return
			case event, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				select {
				case e.observed <- observedEngineEvent{event: event, receivedAt: time.Now().UTC()}:
				default:
				}
				select {
				case events <- event:
				case <-streamCtx.Done():
					return
				}
			case streamErr, ok := <-streamErrors:
				if !ok {
					streamErrors = nil
					continue
				}
				if streamErr != nil {
					select {
					case errorsOut <- streamErr:
					default:
					}
					return
				}
			}
		}
	}()
	closeStream := func() {
		cancel()
		if stream.Close != nil {
			stream.Close()
		}
		<-done
	}
	return EventStream{Events: events, Errors: errorsOut, Close: closeStream}, nil
}

type observedDockerUpdate struct {
	container Container
	arrivedAt time.Time
}

type measurementObserver struct {
	updates chan observedDockerUpdate
}

func (o *measurementObserver) ApplyDockerBatch(ctx context.Context, batch Batch) error {
	if batch.FullSnapshot {
		return nil
	}
	arrivedAt := time.Now().UTC()
	for _, change := range batch.Changes {
		if change.Container == nil {
			continue
		}
		update := observedDockerUpdate{container: cloneContainer(*change.Container), arrivedAt: arrivedAt}
		select {
		case o.updates <- update:
		case <-ctx.Done():
			return ctx.Err()
		default:
			return fmt.Errorf("P95 measurement observer buffer is full")
		}
	}
	return nil
}

func waitForMeasurementEvent(events <-chan observedEngineEvent, id, action string, timeout time.Duration) (observedEngineEvent, time.Time, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case item, ok := <-events:
			if !ok {
				return observedEngineEvent{}, time.Time{}, fmt.Errorf("Docker Engine event stream closed")
			}
			if item.event.ContainerID == id && strings.EqualFold(item.event.Action, action) {
				return item, item.receivedAt, nil
			}
		case <-timer.C:
			return observedEngineEvent{}, time.Time{}, fmt.Errorf("timed out waiting for Docker Engine event %q", action)
		}
	}
}

func waitForMeasurementUpdate(updates <-chan observedDockerUpdate, id string, paused bool, timeout time.Duration) (observedDockerUpdate, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return observedDockerUpdate{}, fmt.Errorf("NodeDance observer closed")
			}
			if update.container.ID == id && update.container.Paused == paused && update.container.State == map[bool]string{true: "paused", false: "running"}[paused] && !update.container.Stale {
				return update, nil
			}
		case <-timer.C:
			return observedDockerUpdate{}, fmt.Errorf("timed out waiting for paused=%t observer update", paused)
		}
	}
}

func durationNearestRank(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	rank := int(float64(len(ordered))*percentile + 0.999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(ordered) {
		rank = len(ordered)
	}
	return ordered[rank-1]
}

func maxDuration(values []time.Duration) time.Duration {
	var maximum time.Duration
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func validSuiteRunID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type controlledEventsEngine struct {
	Engine
	control *firstEventStreamControl
	calls   atomic.Int32
}

type firstEventStreamControl struct {
	first chan func()
}

func (e *controlledEventsEngine) OpenEvents(ctx context.Context) (EventStream, error) {
	stream, err := e.Engine.OpenEvents(ctx)
	if err != nil {
		return EventStream{}, err
	}
	if e.calls.Add(1) == 1 {
		select {
		case e.control.first <- stream.Close:
		case <-ctx.Done():
			if stream.Close != nil {
				stream.Close()
			}
			return EventStream{}, ctx.Err()
		}
	}
	return stream, nil
}

func verifyOwnedDIND(t *testing.T, repoRoot, socket string) string {
	t.Helper()
	var markerPath string
	for _, engine := range []string{"28", "29"} {
		candidate := filepath.Join(repoRoot, ".artifacts", "dind", "v"+engine, "socket", "docker.sock")
		if filepath.Clean(candidate) == filepath.Clean(socket) {
			markerPath = filepath.Join(repoRoot, ".artifacts", "dind", "v"+engine, "owner.json")
			break
		}
	}
	if markerPath == "" {
		t.Fatalf("refusing non-owned Docker socket outside .artifacts/dind: %s", socket)
	}
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read DIND owner marker: %v", err)
	}
	var marker struct {
		Suite         string `json:"suite"`
		Socket        string `json:"socket"`
		ServerVersion string `json:"server_version"`
	}
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatalf("decode DIND owner marker: %v", err)
	}
	if marker.Suite != "nodedance-s00-dind" || filepath.Clean(marker.Socket) != filepath.Clean(socket) || !strings.HasPrefix(marker.ServerVersion, "28.") && !strings.HasPrefix(marker.ServerVersion, "29.") {
		t.Fatalf("DIND owner marker mismatch: %+v", marker)
	}
	return marker.ServerVersion
}

func lockedBusyboxImage(t *testing.T, repoRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "test-images.lock.json"))
	if err != nil {
		t.Fatalf("read locked test images: %v", err)
	}
	var lock struct {
		Images struct {
			Busybox string `json:"busybox"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse locked test images: %v", err)
	}
	if !strings.Contains(lock.Images.Busybox, "@sha256:") {
		t.Fatalf("BusyBox test image is not digest-pinned: %q", lock.Images.Busybox)
	}
	return lock.Images.Busybox
}

func dockerCLI(ctx context.Context, endpoint string, args ...string) (string, error) {
	fullArgs := append([]string{"--host", endpoint}, args...)
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func runDINDHarness(ctx context.Context, repoRoot, action, engine string) (string, error) {
	cmd := exec.CommandContext(ctx, filepath.Join(repoRoot, "scripts", "test", "dind.sh"), action, engine)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("DIND %s %s: %w", action, engine, err)
	}
	return string(output), nil
}

func s04DINDRoot(t *testing.T, repoRoot string) string {
	t.Helper()
	configured := os.Getenv("NODEDANCE_S04_DIND_ROOT")
	if configured == "" {
		return repoRoot
	}
	root, err := filepath.Abs(configured)
	if err != nil {
		t.Fatalf("resolve configured S04 DIND root: %v", err)
	}
	expected := filepath.Join(filepath.Dir(repoRoot), "NodeDance-s04")
	if root != expected {
		t.Fatalf("refusing S04 DIND root outside the designated sibling fixture worktree: %s", root)
	}
	return root
}

func waitDIND(t *testing.T, discoverer *Discoverer, id string, condition func(Container, Health) bool, timeout time.Duration, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		containers, health := discoverer.Current()
		for _, item := range containers {
			if item.ID == id && condition(item, health) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	containers, health := discoverer.Current()
	t.Fatalf("timed out waiting for %s: current=%+v health=%+v", description, containers, health)
}

func waitDINDHealth(t *testing.T, discoverer *Discoverer, condition func(Health) bool, timeout time.Duration, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, health := discoverer.Current()
		if condition(health) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, health := discoverer.Current()
	t.Fatalf("timed out waiting for %s: health=%+v", description, health)
}

func waitForDINDInventory(t *testing.T, discoverer *Discoverer, expectedNames []string, timeout time.Duration) {
	t.Helper()
	expected := make(map[string]struct{}, len(expectedNames))
	for _, name := range expectedNames {
		expected[name] = struct{}{}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		containers, health := discoverer.Current()
		found := make(map[string]struct{}, len(containers))
		for _, item := range containers {
			found[item.Name] = struct{}{}
		}
		complete := health.SnapshotFresh && health.EventsConnected
		for name := range expected {
			if _, ok := found[name]; !ok {
				complete = false
				break
			}
		}
		if complete {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	containers, health := discoverer.Current()
	t.Fatalf("DIND snapshot missed owned fixture(s): expected=%v current=%+v health=%+v", expectedNames, containers, health)
}

func hasMountAt(container Container, destination string) bool {
	for _, mount := range container.Mounts {
		if mount.Destination == destination {
			return true
		}
	}
	return false
}

func findPort(container Container, number uint16, protocol string) *Port {
	for i := range container.Ports {
		if container.Ports[i].ContainerPort == number && container.Ports[i].Protocol == protocol {
			return &container.Ports[i]
		}
	}
	return nil
}

func hasHostBinding(container Container, number uint16, protocol, kind, ip, port string) bool {
	item := findPort(container, number, protocol)
	if item == nil {
		return false
	}
	bindings := item.Configured
	if kind == "published" {
		bindings = item.Published
	}
	for _, binding := range bindings {
		if binding.IP == ip && binding.Port == port {
			return true
		}
	}
	return false
}

func hasPublishedPort(container Container, number uint16, protocol string) bool {
	item := findPort(container, number, protocol)
	if item == nil || len(item.Published) == 0 {
		return false
	}
	for _, binding := range item.Published {
		if binding.Port != "" && binding.Port != "0" {
			return true
		}
	}
	return false
}

func hasPublishedAddress(container Container, number uint16, protocol, ip string) bool {
	item := findPort(container, number, protocol)
	if item == nil {
		return false
	}
	for _, binding := range item.Published {
		if binding.IP == ip && binding.Port != "" && binding.Port != "0" {
			return true
		}
	}
	return false
}

func hasAnyPublishedPorts(container Container) bool {
	for _, item := range container.Ports {
		if len(item.Published) > 0 {
			return true
		}
	}
	return false
}

func portExposedOnly(container Container, number uint16, protocol string) bool {
	item := findPort(container, number, protocol)
	return item != nil && item.Exposed && len(item.Configured) == 0 && len(item.Published) == 0
}
