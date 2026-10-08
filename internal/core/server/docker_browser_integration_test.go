package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// TestRealDockerAgentCoreBrowserP95 measures state changes through the real
// Engine, Agent, Core WebSocket, and rendered browser DOM. The authenticated
// browser runs the embedded production frontend; it is not a route-mocked spec.
func TestRealDockerAgentCoreBrowserP95(t *testing.T) {
	root, endpoint, engineVersion := requireOwnedS04DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	baselineOutput, err := runS04DockerCLI(endpoint, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		t.Fatalf("list baseline containers on the owned Engine: %v", err)
	}
	baselineIDs := strings.Fields(baselineOutput)
	workRoot := filepath.Join(root, ".artifacts", "work-s04")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "browser-fullchain-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	runID := fmt.Sprintf("nd-s04-browser-%d", time.Now().UnixNano())
	containerName := runID + "-created"
	containerID, err := runS04DockerCLI(endpoint, "container", "create", "--name", containerName,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+runID,
		"--expose", "65000/tcp",
		s04BusyboxImage, "sh", "-c", "sleep 300")
	if err != nil {
		t.Fatalf("create exact owner-labelled browser fixture: %v", err)
	}
	containerID = strings.TrimSpace(containerID)
	containerIDs := []string{containerID}
	defer func() {
		for index := len(containerIDs) - 1; index >= 0; index-- {
			id := containerIDs[index]
			label, inspectErr := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{ index .Config.Labels \"io.nodedance.suite\" }}", id)
			if inspectErr != nil || strings.TrimSpace(label) != runID {
				t.Errorf("refusing to remove browser fixture with unverified ownership: id=%s label=%q err=%v", id[:12], strings.TrimSpace(label), inspectErr)
				continue
			}
			if _, removeErr := runS04DockerCLI(endpoint, "container", "rm", "--force", id); removeErr != nil {
				t.Errorf("remove exact browser fixture %s: %v", id[:12], removeErr)
			}
		}
	}()
	createRunningFixture := func(name string, args ...string) string {
		t.Helper()
		command := []string{"container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite=" + runID}
		command = append(command, args...)
		command = append(command, s04BusyboxImage, "sh", "-c", "sleep 300")
		id, createErr := runS04DockerCLI(endpoint, command...)
		if createErr != nil {
			t.Fatalf("create exact browser Engine fixture %s: %v", name, createErr)
		}
		id = strings.TrimSpace(id)
		containerIDs = append(containerIDs, id)
		return id
	}
	portsID := createRunningFixture(runID+"-ports",
		"--publish", "127.0.0.1:18080:80/tcp",
		"--publish", "127.0.0.1:18053:53/udp",
		"--publish", "[::1]:18443:443/tcp",
		"--publish", "127.0.0.1::8080/tcp",
		"--expose", "65001/tcp")
	hostID := createRunningFixture(runID+"-host", "--network", "host")
	// The shared Compose fixture owns 18081. Request an Engine-assigned host
	// port so this stopped-container case remains isolated from that fixture.
	stoppedID := createRunningFixture(runID+"-stopped", "--publish", "127.0.0.1::81/tcp")
	if _, err := runS04DockerCLI(endpoint, "container", "stop", "--time", "0", stoppedID); err != nil {
		t.Fatalf("stop exact browser fixture: %v", err)
	}
	healthID := createRunningFixture(runID+"-health", "--health-cmd", "test ! -f /tmp/nodedance-unhealthy",
		"--health-interval", "1s", "--health-timeout", "1s", "--health-retries", "2")
	waitEngineHealth := func(want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			got, inspectErr := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", healthID)
			if inspectErr == nil && strings.TrimSpace(got) == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		got, inspectErr := runS04DockerCLI(endpoint, "container", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", healthID)
		t.Fatalf("browser health fixture did not reach %s: got=%q err=%v", want, strings.TrimSpace(got), inspectErr)
	}
	waitEngineHealth("healthy")

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	core, coreURL, closeCore := startS04BrowserCore(t, filepath.Join(work, "core"), certificate)
	defer closeCore()
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S04 real browser Core-Agent-Engine "+engineVersion,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreURL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("enroll real Agent to the same TLS Core served to the browser: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cancelAgent context.CancelFunc
	var agentDone chan error
	startAgent := func() {
		agentCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		cancelAgent, agentDone = cancel, done
		go func() { done <- agent.Run(agentCtx, configPath, "s04-browser-fullchain", &agentTestLog{}) }()
	}
	stopAgent := func() error {
		if cancelAgent == nil {
			return nil
		}
		cancel, done := cancelAgent, agentDone
		cancelAgent, agentDone = nil, nil
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return errors.New("real Agent did not stop after browser full-chain test cancellation")
		}
	}
	startAgent()
	defer func() {
		if err := stopAgent(); err != nil {
			t.Errorf("stop real Agent after browser full-chain test: %v", err)
		}
	}()

	waitForAgentStatus(t, core, config.NodeID, "online", 0)
	if _, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, containerID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "created" && !record.Container.Running && !record.Container.Stale
	}, 30*time.Second); err != nil {
		t.Fatalf("Core did not publish the real created-only Engine fixture: %v", err)
	}
	createdRecord, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, containerID, func(record coredocker.ContainerRecord) bool {
		return record.Container.CreatedAt != nil && record.Container.StartedAt == nil && record.Container.FinishedAt == nil
	}, 10*time.Second)
	if err != nil || createdRecord.Container.State != "created" || !browserHasPort(createdRecord, 65000, "tcp", func(port protocol.DockerPort) bool {
		return port.Exposed && len(port.Published) == 0
	}) {
		t.Fatalf("Core API lost the never-started/expose-only distinction: record=%+v err=%v", createdRecord, err)
	}
	portsRecord, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, portsID, func(record coredocker.ContainerRecord) bool {
		return record.Container.Running && len(record.Container.Ports) >= 5
	}, 30*time.Second)
	if err != nil || !browserHasPort(portsRecord, 80, "tcp", func(port protocol.DockerPort) bool {
		return browserHasBinding(port.Published, "127.0.0.1", "18080")
	}) || !browserHasPort(portsRecord, 53, "udp", func(port protocol.DockerPort) bool {
		return browserHasBinding(port.Published, "127.0.0.1", "18053")
	}) || !browserHasPort(portsRecord, 443, "tcp", func(port protocol.DockerPort) bool {
		return browserHasBinding(port.Published, "::1", "18443")
	}) || !browserHasPort(portsRecord, 8080, "tcp", func(port protocol.DockerPort) bool {
		return len(port.Published) == 1 && port.Published[0].IP == "127.0.0.1" && port.Published[0].Port != "" && port.Published[0].Port != "0"
	}) || !browserHasPort(portsRecord, 65001, "tcp", func(port protocol.DockerPort) bool {
		return port.Exposed && len(port.Published) == 0
	}) {
		t.Fatalf("Core API did not preserve TCP/UDP/IPv4/IPv6/dynamic/EXPOSE port states: record=%+v err=%v", portsRecord, err)
	}
	hostRecord, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, hostID, func(record coredocker.ContainerRecord) bool {
		return record.Container.Running && record.Container.HostNetwork
	}, 30*time.Second)
	if err != nil || hasAnyBrowserPublishedPort(hostRecord) {
		t.Fatalf("Core API reported a false bridge publication for Host Network: record=%+v err=%v", hostRecord, err)
	}
	stoppedRecord, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, stoppedID, func(record coredocker.ContainerRecord) bool {
		return record.Container.State == "exited" && !record.Container.Running
	}, 30*time.Second)
	if err != nil || hasAnyBrowserPublishedPort(stoppedRecord) {
		t.Fatalf("Core API reported an active publication for a stopped container: record=%+v err=%v", stoppedRecord, err)
	}
	healthRecord, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, healthID, func(record coredocker.ContainerRecord) bool {
		return record.Container.HealthcheckConfigured && record.Container.Health == protocol.DockerHealthHealthy
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("Core API did not report the actual Healthy Docker healthcheck: %+v err=%v", healthRecord, err)
	}

	bridge := startS04BrowserBridge(t, filepath.Join(work, "browser-config.json"), s04BrowserConfig{
		URL: coreURL, Session: session, NodeID: config.NodeID, ContainerID: containerID,
		ContainerName: containerName, HealthContainerName: runID + "-health", DockerEndpoint: endpoint,
	})
	defer bridge.Close()
	expectedRenderedIDs := append(append([]string(nil), baselineIDs...), containerIDs...)
	t.Logf("real browser Engine inventory: baseline=%d testOwned=%d Engine=%s", len(baselineIDs), len(containerIDs), engineVersion)

	opened, err := bridge.Call("open")
	if err != nil {
		t.Fatalf("real browser could not authenticate, load the embedded Core UI, and render Agent inventory: %v", err)
	}
	t.Logf("S04_BROWSER opened real Core page: browser=%s initial_container_rows=%v unauthorized_api_status=%v", opened.Browser, opened.InitialRows, opened.UnauthorizedStatus)
	if opened.UnauthorizedStatus != http.StatusUnauthorized || opened.InitialRows != len(expectedRenderedIDs) {
		t.Fatalf("real browser authorization/render contract failed: unauthorized_api=%d rows=%d", opened.UnauthorizedStatus, opened.InitialRows)
	}
	if len(opened.ContainerIDs) != opened.InitialRows || !sameS04StringSet(opened.ContainerIDs, expectedRenderedIDs) {
		t.Fatalf("real browser did not render the exact initial Core container IDs: IDs=%v rows=%d expected=%v", opened.ContainerIDs, opened.InitialRows, expectedRenderedIDs)
	}
	if !browserRenderedPort(opened.Rows, "127.0.0.1:18080 → 80/tcp") || !browserRenderedPort(opened.Rows, "127.0.0.1:18053 → 53/udp") ||
		!browserRenderedPort(opened.Rows, "[::1]:18443 → 443/tcp") || !browserRenderedDynamicPort(opened.Rows, 8080) ||
		!browserRenderedPort(opened.Rows, "65000/tcp（仅声明）") || !browserRenderedHealth(opened.Rows, "健康：健康") {
		t.Fatalf("real browser did not render the Core port/health inventory fields: rows=%+v", opened.Rows)
	}
	t.Logf("S04_BROWSER real inventory fields rendered: created=%s ports=%s host-network=%s stopped=%s health=%s", containerID[:12], portsID[:12], hostID[:12], stoppedID[:12], healthID[:12])

	if _, err := runS04DockerCLI(endpoint, "container", "exec", healthID, "touch", "/tmp/nodedance-unhealthy"); err != nil {
		t.Fatalf("trigger a real unhealthy Docker healthcheck: %v", err)
	}
	unhealthy, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, healthID, func(record coredocker.ContainerRecord) bool {
		return record.Container.Health == protocol.DockerHealthUnhealthy
	}, 15*time.Second)
	if err != nil || unhealthy.Container.Health != protocol.DockerHealthUnhealthy {
		t.Fatalf("Core API did not report the real Unhealthy healthcheck: %+v err=%v", unhealthy, err)
	}
	if _, err := bridge.Call("waitHealth", "健康：异常"); err != nil {
		t.Fatalf("real browser did not render Unhealthy health state: %v", err)
	}
	if _, err := runS04DockerCLI(endpoint, "container", "exec", healthID, "rm", "-f", "/tmp/nodedance-unhealthy"); err != nil {
		t.Fatalf("restore the real healthcheck fixture: %v", err)
	}
	if _, err := waitForS04ContainerRecordAPI(coreURL, rootPEM, session, config.NodeID, healthID, func(record coredocker.ContainerRecord) bool {
		return record.Container.Health == protocol.DockerHealthHealthy
	}, 15*time.Second); err != nil {
		t.Fatalf("Core API did not restore the Healthy healthcheck: %v", err)
	}
	if _, err := bridge.Call("waitHealth", "健康：健康"); err != nil {
		t.Fatalf("real browser did not render recovered Healthy state: %v", err)
	}

	responsive, err := bridge.Call("responsive")
	if err != nil {
		t.Fatalf("real rendered dashboard failed responsive viewport checks: %v", err)
	}
	t.Logf("S04_BROWSER responsive real page widths validated: %v", strings.Join(responsive.Viewports, ","))

	// Exercise a real Engine outage while the authenticated page remains open.
	engine := strings.SplitN(engineVersion, ".", 2)[0]
	dindScriptRoot := root
	if configuredRoot := os.Getenv("NODEDANCE_S04_DIND_ROOT"); configuredRoot != "" {
		dindScriptRoot, err = filepath.Abs(configuredRoot)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := runS04DINDScript(dindScriptRoot, "stop", engine); err != nil {
		t.Fatalf("stop the exact marker-owned nested Engine during real browser test: %v", err)
	}
	dindStopped := true
	defer func() {
		if dindStopped {
			if err := runS04DINDScript(dindScriptRoot, "start", engine); err != nil {
				t.Errorf("restart exact marker-owned nested Engine during cleanup: %v", err)
			}
		}
	}()
	heartbeatBeforeStop := heartbeatSequenceForNode(t, core, config.NodeID)
	failed, err := bridge.Call("waitUnavailable")
	if err != nil {
		t.Fatalf("real browser did not display Docker-unavailable/stale while Agent continued: %v", err)
	}
	heartbeatAfterStop := heartbeatSequenceForNode(t, core, config.NodeID)
	waitForCondition(t, 12*time.Second, func() bool {
		heartbeatAfterStop = heartbeatSequenceForNode(t, core, config.NodeID)
		return heartbeatAfterStop > heartbeatBeforeStop
	}, "real Agent heartbeat did not advance while Docker Engine was unavailable")
	t.Logf("S04_BROWSER failure state visible: %s rows=%d staleRows=%d retainedIDs=%v; Agent heartbeat advanced %d→%d", failed.DockerState, failed.InitialRows, failed.StaleRows, failed.ContainerIDs, heartbeatBeforeStop, heartbeatAfterStop)
	if heartbeatAfterStop <= heartbeatBeforeStop {
		t.Fatal("real browser failure-state check did not observe continued Agent heartbeat")
	}
	if failed.InitialRows != opened.InitialRows || failed.StaleRows != opened.InitialRows || strings.Join(failed.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("Docker unavailable UI hid or changed historical assets: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, failed.ContainerIDs, failed.InitialRows, failed.StaleRows)
	}
	if err := runS04DINDScript(dindScriptRoot, "start", engine); err != nil {
		t.Fatalf("restart the exact marker-owned nested Engine after browser failure assertion: %v", err)
	}
	dindStopped = false
	recovered, err := bridge.Call("waitFresh")
	if err != nil {
		t.Fatalf("real browser did not display fresh inventory after Engine recovery: %v", err)
	}
	t.Logf("S04_BROWSER recovered state visible: %s rows=%v staleRows=%d IDs=%v", recovered.DockerState, recovered.InitialRows, recovered.StaleRows, recovered.ContainerIDs)
	if recovered.InitialRows != opened.InitialRows || recovered.StaleRows != 0 || strings.Join(recovered.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("recovered browser inventory did not return the same fresh assets: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, recovered.ContainerIDs, recovered.InitialRows, recovered.StaleRows)
	}

	// A lost Agent lease makes the last Docker state stale in both API and UI,
	// even if no later Docker event arrives to mutate the inventory revision.
	if err := stopAgent(); err != nil {
		t.Fatalf("stop the real Agent to exercise lease expiry: %v", err)
	}
	heartbeatAtAgentDisconnect := heartbeatSequenceForNode(t, core, config.NodeID)
	agentOffline, err := bridge.Call("waitAgentOffline")
	if err != nil {
		t.Fatalf("real browser did not mark retained Docker state stale when the Agent lease expired: %v", err)
	}
	if agentOffline.InitialRows != opened.InitialRows || agentOffline.StaleRows != opened.InitialRows ||
		strings.Join(agentOffline.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("Agent lease expiry hid or changed historical Docker assets: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, agentOffline.ContainerIDs, agentOffline.InitialRows, agentOffline.StaleRows)
	}
	if after := heartbeatSequenceForNode(t, core, config.NodeID); after != heartbeatAtAgentDisconnect {
		t.Fatalf("Agent heartbeat advanced after the real Agent stopped: stopped_at=%d after_lease_expiry=%d", heartbeatAtAgentDisconnect, after)
	}
	t.Logf("S04_BROWSER Agent lease expiry kept the same %d historical rows visible and stale; Agent remained offline at heartbeat=%d", agentOffline.InitialRows, heartbeatAtAgentDisconnect)
	startAgent()
	waitForAgentStatusWithin(t, core, config.NodeID, "online", 0, 20*time.Second)
	leaseRecovered, err := bridge.Call("waitFresh")
	if err != nil {
		t.Fatalf("real browser did not freshen the retained inventory after Agent reconnect: %v", err)
	}
	if leaseRecovered.InitialRows != opened.InitialRows || leaseRecovered.StaleRows != 0 || strings.Join(leaseRecovered.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("Agent reconnect did not preserve and refresh the same inventory: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, leaseRecovered.ContainerIDs, leaseRecovered.InitialRows, leaseRecovered.StaleRows)
	}

	networkLease, err := bridge.Call("simulateNetworkLoss")
	if err != nil {
		t.Fatalf("real browser could not establish the Core lease baseline and cut HTTP/WSS routes: %v", err)
	}
	networkStale, err := bridge.Call("waitLeaseStale")
	if err != nil {
		t.Fatalf("real browser did not locally stale the last Docker snapshot after HTTP and WSS loss: %v", err)
	}
	if networkStale.InitialRows != opened.InitialRows || networkStale.StaleRows != opened.InitialRows ||
		strings.Join(networkStale.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("browser network loss hid or changed historical Docker assets: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, networkStale.ContainerIDs, networkStale.InitialRows, networkStale.StaleRows)
	}
	if networkStale.PredictedRemainingMS != networkLease.PredictedRemainingMS {
		t.Fatalf("browser lease baseline changed during the simulated outage: start=%dms stale-check=%dms", networkLease.PredictedRemainingMS, networkStale.PredictedRemainingMS)
	}
	if networkStale.HTTPFailures == 0 || networkStale.WebSocketFailures == 0 {
		t.Fatalf("browser did not actually lose both Core channels: HTTP failures=%d WebSocket failures=%d", networkStale.HTTPFailures, networkStale.WebSocketFailures)
	}
	if networkStale.ElapsedMS < networkStale.PredictedRemainingMS-500 {
		t.Fatalf("browser displayed Docker stale before the Core-derived lease elapsed: elapsed=%dms predicted_remaining=%dms", networkStale.ElapsedMS, networkStale.PredictedRemainingMS)
	}
	if networkStale.ElapsedMS > networkStale.PredictedRemainingMS+1_000 {
		t.Fatalf("browser kept Docker fresh beyond lease expiry plus four UI ticks: elapsed=%dms predicted_remaining=%dms", networkStale.ElapsedMS, networkStale.PredictedRemainingMS)
	}
	t.Logf("S04_BROWSER HTTP+WSS loss retained %d stale rows; performance-clock lease remaining=%dms, stale at %dms, HTTP failures=%d WSS failures=%d", networkStale.InitialRows, networkStale.PredictedRemainingMS, networkStale.ElapsedMS, networkStale.HTTPFailures, networkStale.WebSocketFailures)
	networkRecovered, err := bridge.Call("restoreNetwork")
	if err != nil {
		t.Fatalf("real browser did not recover its API/WSS channels: %v", err)
	}
	if networkRecovered.InitialRows != opened.InitialRows || networkRecovered.StaleRows != 0 || strings.Join(networkRecovered.ContainerIDs, ",") != strings.Join(opened.ContainerIDs, ",") {
		t.Fatalf("browser network recovery did not freshen the same historical inventory: before=%v after=%v rows=%d staleRows=%d", opened.ContainerIDs, networkRecovered.ContainerIDs, networkRecovered.InitialRows, networkRecovered.StaleRows)
	}

	logS04BrowserHostLoad(t, engineVersion, "before")
	measurement, err := bridge.Call("measure100")
	if err != nil {
		t.Fatalf("real browser DOM did not converge through 100 external Engine changes: %v", err)
	}
	if len(measurement.Samples) != 100 {
		t.Fatalf("browser DOM measurement recorded %d samples, want exactly 100", len(measurement.Samples))
	}
	for _, sample := range measurement.Samples {
		t.Logf("S04_BROWSER_STATE index=%03d action=%s engine_request_at=%s dom_observed_at=%s latency_ms=%d", sample.Index, sample.Action, sample.RequestedAt, sample.ObservedAt, sample.LatencyMS)
		if sample.LatencyMS > 1_000 {
			logS04BrowserHostLoad(t, engineVersion, fmt.Sprintf("slow_sample_%03d_%dms", sample.Index, sample.LatencyMS))
		}
	}
	logS04BrowserHostLoad(t, engineVersion, "after")
	t.Logf("S04_SUP_01 real Core-Agent-Engine-browser DOM latency summary: Engine=%s count=%d missed=%d min_ms=%d mean_ms=%d p50_ms=%d p95_ms=%d max_ms=%d", engineVersion, measurement.Count, measurement.Missed, measurement.MinMS, measurement.MeanMS, measurement.P50MS, measurement.P95MS, measurement.MaxMS)
	if measurement.Missed != 0 || measurement.P95MS > 5_000 {
		t.Fatalf("real browser state sync missed=%d p95=%dms; acceptance is missed=0 and P95<=5000ms", measurement.Missed, measurement.P95MS)
	}
}

func logS04BrowserHostLoad(t *testing.T, engineVersion, point string) {
	t.Helper()
	loadAverage := "unavailable"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		loadAverage = strings.TrimSpace(string(data))
	}
	memAvailable := "unavailable"
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemAvailable:") {
				memAvailable = strings.TrimSpace(strings.TrimPrefix(line, "MemAvailable:"))
				break
			}
		}
	}
	t.Logf("S04_BROWSER_HOST_ENV point=%s engine=%s goos=%s goarch=%s cpu_count=%d gomaxprocs=%d loadavg=%q memavailable=%q", point, engineVersion, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), loadAverage, memAvailable)
}

func startS04BrowserCore(t *testing.T, dataDir string, certificate tls.Certificate) (*Server, string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coreURL := "https://" + listener.Addr().String()
	core, err := New("s04-real-browser", Options{DataDir: dataDir, PublicOrigin: coreURL, WebSocketCheckInterval: 100 * time.Millisecond})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: core, TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	}}
	tlsListener := tls.NewListener(listener, httpServer.TLSConfig)
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(tlsListener) }()
	closeCore := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("shutdown real Core HTTPS listener: %v", err)
		}
		if err := core.Close(); err != nil {
			t.Errorf("close real Core after browser full-chain test: %v", err)
		}
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("real Core HTTPS Serve ended with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("real Core HTTPS Serve goroutine did not exit")
		}
	}
	return core, coreURL, closeCore
}

type s04BrowserConfig struct {
	URL                 string `json:"url"`
	Session             string `json:"session"`
	NodeID              string `json:"nodeId"`
	ContainerID         string `json:"containerId"`
	ContainerName       string `json:"containerName"`
	HealthContainerName string `json:"healthContainerName"`
	DockerEndpoint      string `json:"dockerEndpoint"`
}

type s04BrowserSample struct {
	Index       int    `json:"index"`
	Action      string `json:"action"`
	RequestedAt string `json:"requestedAt"`
	ObservedAt  string `json:"observedAt"`
	LatencyMS   int64  `json:"latencyMs"`
}

type s04BrowserRow struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	State  string   `json:"state"`
	Health string   `json:"health"`
	Ports  []string `json:"ports"`
}

type s04BrowserResult struct {
	Browser              string             `json:"browser"`
	InitialRows          int                `json:"initialRows"`
	ContainerIDs         []string           `json:"containerIDs"`
	Rows                 []s04BrowserRow    `json:"rows"`
	StaleRows            int                `json:"staleRows"`
	UnauthorizedStatus   int                `json:"unauthorizedStatus"`
	DockerState          string             `json:"dockerState"`
	HeartbeatAdvanced    bool               `json:"heartbeatAdvanced"`
	Viewports            []string           `json:"viewports"`
	Count                int                `json:"count"`
	Missed               int                `json:"missed"`
	MinMS                int64              `json:"minMs"`
	MeanMS               int64              `json:"meanMs"`
	P50MS                int64              `json:"p50Ms"`
	P95MS                int64              `json:"p95Ms"`
	MaxMS                int64              `json:"maxMs"`
	ElapsedMS            int64              `json:"elapsedMs"`
	PredictedRemainingMS int64              `json:"predictedRemainingMs"`
	HTTPFailures         int                `json:"httpFailures"`
	WebSocketFailures    int                `json:"websocketFailures"`
	Samples              []s04BrowserSample `json:"samples"`
	Error                string             `json:"error"`
}

type s04BrowserBridge struct {
	t          *testing.T
	cmd        *exec.Cmd
	configPath string
	closeStdin io.Closer
	stdin      *bufio.Writer
	stdout     *bufio.Scanner
	seq        int
}

func startS04BrowserBridge(t *testing.T, configPath string, config s04BrowserConfig) *s04BrowserBridge {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "web", "tests", "s04-real-browser.mjs")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("locked Node executable unavailable for real browser harness: %v", err)
	}
	cmd := exec.Command(node, script, configPath)
	cmd.Dir = filepath.Join(root, "web")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start locked real-browser harness: %v", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	bridge := &s04BrowserBridge{t: t, cmd: cmd, configPath: configPath, closeStdin: stdinPipe, stdin: bufio.NewWriter(stdinPipe), stdout: scanner}
	t.Cleanup(func() { bridge.Close() })
	return bridge
}

func (b *s04BrowserBridge) Call(action string, arguments ...any) (s04BrowserResult, error) {
	b.t.Helper()
	b.seq++
	requestValue := map[string]any{"id": b.seq, "action": action}
	if len(arguments) > 0 {
		requestValue["argument"] = arguments[0]
	}
	request, err := json.Marshal(requestValue)
	if err != nil {
		return s04BrowserResult{}, err
	}
	if _, err := b.stdin.Write(append(request, '\n')); err != nil {
		return s04BrowserResult{}, fmt.Errorf("write real-browser command: %w", err)
	}
	if err := b.stdin.Flush(); err != nil {
		return s04BrowserResult{}, fmt.Errorf("flush real-browser command: %w", err)
	}
	if !b.stdout.Scan() {
		if err := b.stdout.Err(); err != nil {
			return s04BrowserResult{}, fmt.Errorf("read real-browser response: %w", err)
		}
		return s04BrowserResult{}, errors.New("real-browser harness exited without a response")
	}
	var response struct {
		ID     int              `json:"id"`
		Result s04BrowserResult `json:"result"`
		Error  string           `json:"error"`
	}
	if err := json.Unmarshal(b.stdout.Bytes(), &response); err != nil {
		return s04BrowserResult{}, fmt.Errorf("decode real-browser response: %w", err)
	}
	if response.ID != b.seq {
		return s04BrowserResult{}, fmt.Errorf("real-browser response ID=%d, want %d", response.ID, b.seq)
	}
	if response.Error != "" {
		return response.Result, errors.New(response.Error)
	}
	return response.Result, nil
}

func (b *s04BrowserBridge) Close() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	_, _ = b.Call("close")
	if b.closeStdin != nil {
		_ = b.closeStdin.Close()
	}
	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			b.t.Errorf("real-browser harness exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = b.cmd.Process.Kill()
		b.t.Error("real-browser harness did not exit after close")
	}
	b.cmd = nil
	if b.configPath != "" {
		_ = os.Remove(b.configPath)
		b.configPath = ""
	}
}

func browserHasPort(record coredocker.ContainerRecord, containerPort uint16, protocolName string, check func(protocol.DockerPort) bool) bool {
	for _, port := range record.Container.Ports {
		if port.ContainerPort == containerPort && port.Protocol == protocolName && check(port) {
			return true
		}
	}
	return false
}

func browserHasBinding(bindings []protocol.DockerHostPort, ip, port string) bool {
	for _, binding := range bindings {
		if binding.IP == ip && binding.Port == port {
			return true
		}
	}
	return false
}

func hasAnyBrowserPublishedPort(record coredocker.ContainerRecord) bool {
	for _, port := range record.Container.Ports {
		if len(port.Published) != 0 {
			return true
		}
	}
	return false
}

func browserRenderedPort(rows []s04BrowserRow, expected string) bool {
	for _, row := range rows {
		for _, port := range row.Ports {
			if port == expected {
				return true
			}
		}
	}
	return false
}

func browserRenderedDynamicPort(rows []s04BrowserRow, containerPort uint16) bool {
	suffix := fmt.Sprintf(" → %d/tcp", containerPort)
	for _, row := range rows {
		for _, port := range row.Ports {
			if !strings.HasPrefix(port, "127.0.0.1:") || !strings.HasSuffix(port, suffix) {
				continue
			}
			hostPort := strings.TrimSuffix(strings.TrimPrefix(port, "127.0.0.1:"), suffix)
			if number, err := strconv.Atoi(hostPort); err == nil && number > 0 && number <= 65535 {
				return true
			}
		}
	}
	return false
}

func browserRenderedHealth(rows []s04BrowserRow, expected string) bool {
	for _, row := range rows {
		if row.Health == expected {
			return true
		}
	}
	return false
}

func sameS04StringSet(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s s04BrowserSample) String() string {
	return fmt.Sprintf("%03d %s %s→%s %dms", s.Index, s.Action, s.RequestedAt, s.ObservedAt, s.LatencyMS)
}

func runS04DockerCompose(projectDirectory string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	composeArgs := append([]string{"compose", "--project-directory", projectDirectory}, args...)
	command := exec.CommandContext(ctx, "docker", composeArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker compose --project-directory <owned-s04-fixture> %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func removeS04FixtureID(ids []string, remove string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != remove {
			result = append(result, id)
		}
	}
	return result
}

func waitForS04ContainerRecordAPI(serverURL string, rootPEM []byte, session, nodeID, containerID string, predicate func(coredocker.ContainerRecord) bool, timeout time.Duration) (coredocker.ContainerRecord, error) {
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 3 * time.Second
	path := serverURL + "/api/v1/nodes/" + nodeID + "/containers/" + containerID
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			return coredocker.ContainerRecord{}, err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var detail struct {
			Container *coredocker.ContainerRecord `json:"container"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&detail)
		response.Body.Close()
		if response.StatusCode == http.StatusOK && decodeErr == nil && detail.Container != nil {
			if detail.Container.Container.ID != containerID {
				return coredocker.ContainerRecord{}, fmt.Errorf("Core detail API returned container %q for requested %q", detail.Container.Container.ID, containerID)
			}
			if predicate(*detail.Container) {
				return *detail.Container, nil
			}
		} else {
			lastErr = fmt.Errorf("Core detail API status=%d decode=%v", response.StatusCode, decodeErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return coredocker.ContainerRecord{}, fmt.Errorf("container record did not converge before timeout: %w", lastErr)
}

func waitForS04ContainerAbsentAPI(serverURL string, rootPEM []byte, session, nodeID, containerID string, timeout time.Duration) error {
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 3 * time.Second
	path := serverURL + "/api/v1/nodes/" + nodeID + "/containers/" + containerID
	deadline := time.Now().Add(timeout)
	var lastStatus int
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err == nil {
			lastStatus = response.StatusCode
			response.Body.Close()
			if response.StatusCode == http.StatusNotFound {
				return nil
			}
		} else {
			lastStatus = 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("deleted container detail remained visible or unavailable: final HTTP status=%d", lastStatus)
}

func waitForS04DashboardInventoryAbsent(t *testing.T, events <-chan s04DashboardRead, nodeID, containerID string, timeout time.Duration) (coredocker.View, error) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || event.err != nil {
				return coredocker.View{}, fmt.Errorf("Dashboard closed while waiting for deleted container push: %v", event.err)
			}
			if event.view.NodeID == nodeID && !dockerViewContains(event.view, containerID) {
				return event.view, nil
			}
		case <-timer.C:
			return coredocker.View{}, errors.New("Dashboard did not remove the deleted container before timeout")
		}
	}
}
