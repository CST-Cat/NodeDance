package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
)

const (
	s06DINDRunIDEnv  = "NODEDANCE_S06_RUN_ID"
	s06DINDRootEnv   = "NODEDANCE_S06_DIND_ROOT"
	s06DINDHostEnv   = "NODEDANCE_S06_DIND_HOST"
	s06DINDSuite     = "nodedance-s06-dashboard-fixture"
	s06DINDContainer = 40
)

// TestS06RealDashboardOwnedDINDResponsiveAndTouch covers only the real
// Engine-backed S06 dashboard, responsive browser, and touch reorder cases.
// Its browser serves the embedded Core assets and uses the real authenticated
// Core APIs and Agent WebSocket; the test does not install routes or API mocks.
func TestS06RealDashboardOwnedDINDResponsiveAndTouch(t *testing.T) {
	root, runRoot, endpoint, runID, engineVersion := requireOwnedS06DIND(t)
	t.Setenv("DOCKER_HOST", endpoint)
	if engineVersion != "29.7.2" {
		t.Fatalf("S06 real dashboard acceptance requires the locked Engine 29.7.2, got %q", engineVersion)
	}

	baselineIDs, err := s06DockerContainerIDs(endpoint)
	if err != nil {
		t.Fatalf("read the exact Engine 29 baseline inventory: %v", err)
	}
	if len(baselineIDs) != 0 {
		t.Fatalf("fresh run-owned S06 Engine contains %d baseline containers; refusing to adopt them: %v", len(baselineIDs), baselineIDs)
	}
	if _, err := s06DockerCLI(endpoint, "image", "inspect", s04BusyboxImage); err != nil {
		if _, pullErr := s06DockerCLI(endpoint, "image", "pull", s04BusyboxImage); pullErr != nil {
			t.Fatalf("obtain the locked fixture image on the owned Engine: %v", pullErr)
		}
	}

	fixtureRunID := "s06-" + runID
	fixtureIDs := make([]string, 0, s06DINDContainer)
	fixtureNames := make([]s06ExpectedContainer, 0, s06DINDContainer)
	cleanupFixtures := func() {
		for index := len(fixtureIDs) - 1; index >= 0; index-- {
			id := fixtureIDs[index]
			inspection, inspectErr := s06DockerCLI(endpoint, "container", "inspect", "--format",
				`{{.Id}}|{{.Name}}|{{index .Config.Labels "io.nodedance.test"}}|{{index .Config.Labels "io.nodedance.suite"}}|{{index .Config.Labels "io.nodedance.run"}}`, id)
			if inspectErr != nil {
				// The exact ID is already absent; the postcondition below will prove
				// that no owned resource remains and the baseline is unchanged.
				continue
			}
			parts := strings.Split(strings.TrimSpace(inspection), "|")
			if len(parts) != 5 || parts[0] != id || strings.TrimPrefix(parts[1], "/") != fixtureNames[index].Name ||
				parts[2] != "true" || parts[3] != s06DINDSuite || parts[4] != fixtureRunID {
				t.Errorf("refusing to remove a container without this S06 run's exact owner labels: id=%s inspection_fields=%d", id[:12], len(parts))
				continue
			}
			if _, removeErr := s06DockerCLI(endpoint, "container", "rm", "--force", id); removeErr != nil {
				t.Errorf("remove exact S06-owned container %s: %v", id[:12], removeErr)
			}
		}
		remainingOwned, listErr := s06DockerContainerIDs(endpoint,
			"--filter", "label=io.nodedance.suite="+s06DINDSuite,
			"--filter", "label=io.nodedance.run="+fixtureRunID)
		if listErr != nil {
			t.Errorf("verify S06-owned fixture cleanup: %v", listErr)
		} else if len(remainingOwned) != 0 {
			t.Errorf("S06 fixture cleanup left owned containers behind: %v", shortenedS06IDs(remainingOwned))
		}
		afterIDs, afterErr := s06DockerContainerIDs(endpoint)
		if afterErr != nil {
			t.Errorf("verify Engine inventory after S06 fixture cleanup: %v", afterErr)
		} else if !equalS06IDs(afterIDs, baselineIDs) {
			t.Errorf("S06 cleanup changed the exact Engine baseline: before=%v after=%v", baselineIDs, shortenedS06IDs(afterIDs))
		} else if len(fixtureIDs) == s06DINDContainer {
			t.Logf("S06_FIXTURE_CLEANUP_PASS removed=%d owner_marked=true baseline_retained=%d foreign_resources_touched=0", len(fixtureIDs), len(baselineIDs))
		}
	}
	t.Cleanup(cleanupFixtures)

	for index := 0; index < s06DINDContainer; index++ {
		name := fmt.Sprintf("nodedance-s06-%s-%02d", runID, index)
		created, createErr := s06DockerCLI(endpoint, "container", "run", "--detach", "--name", name,
			"--label", "io.nodedance.test=true",
			"--label", "io.nodedance.suite="+s06DINDSuite,
			"--label", "io.nodedance.run="+fixtureRunID,
			s04BusyboxImage, "sh", "-c", "while :; do sleep 3600; done")
		if createErr != nil {
			t.Fatalf("create exact owner-marked S06 container %02d: %v", index, createErr)
		}
		id := strings.TrimSpace(created)
		if len(id) != 64 {
			t.Fatalf("Engine returned an invalid full container ID for S06 fixture %02d", index)
		}
		fixtureIDs = append(fixtureIDs, id)
		fixtureNames = append(fixtureNames, s06ExpectedContainer{ID: id, Name: name})
	}
	engineIDs, err := s06DockerContainerIDs(endpoint)
	if err != nil {
		t.Fatalf("read Engine inventory after creating the 40 S06 fixtures: %v", err)
	}
	wantEngineIDs := append(append([]string(nil), baselineIDs...), fixtureIDs...)
	if !equalS06IDs(engineIDs, wantEngineIDs) {
		t.Fatalf("run-owned Engine inventory is not exactly baseline plus the 40 S06 containers: got=%v want_count=%d", shortenedS06IDs(engineIDs), len(wantEngineIDs))
	}
	t.Logf("S06_FIXTURE_ENGINE_PASS owner_marked_containers=%d baseline=%d Engine=%s", len(fixtureIDs), len(baselineIDs), engineVersion)

	workDir := filepath.Join(runRoot, "test-work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create unique S06 run work directory under the owner marker: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(workDir); err != nil {
			t.Errorf("remove only this S06 run's temporary Core/Agent credentials: %v", err)
		}
	})
	if err := os.Mkdir(filepath.Join(workDir, "agent"), 0o700); err != nil {
		t.Fatal("create private Agent credential directory:", err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatalf("create local HTTPS test CA: %v", err)
	}
	core, coreURL, closeCore := startS04BrowserCore(t, filepath.Join(workDir, "core"), certificate)
	t.Cleanup(closeCore)
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatalf("create authenticated browser session for the real Core APIs: %v", err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S06 real Engine responsive dashboard "+runID,
		"127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatalf("create one-time Agent enrollment on this real Core: %v", err)
	}
	caPath := filepath.Join(workDir, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal("write private test CA:", err)
	}
	configPath := filepath.Join(workDir, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreURL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("register a real Agent over HTTPS to this Core: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load the real enrolled Agent configuration: %v", err)
	}
	if config.NodeID == "" || config.AgentID == "" {
		t.Fatal("real Agent enrollment did not persist its assigned identity")
	}

	settingsResponse, settingsBody := s06AuthenticatedRequest(t, coreURL, rootPEM, session, http.MethodGet, "/api/v1/dashboard/settings", nil, "")
	if settingsResponse.StatusCode != http.StatusOK {
		t.Fatalf("authenticated Core dashboard settings API returned HTTP %d", settingsResponse.StatusCode)
	}
	var settings dashboard.Settings
	if err := json.Unmarshal(settingsBody, &settings); err != nil || settings.FeaturedLimit != 4 || settings.SortBy != "custom" {
		t.Fatalf("Core default dashboard settings do not configure four custom-sorted featured containers: featured_limit=%d sort_by=%q decode_error=%v", settings.FeaturedLimit, settings.SortBy, err)
	}

	var agentCancel context.CancelFunc
	var agentDone chan error
	startAgent := func() {
		agentCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		agentCancel, agentDone = cancel, done
		go func() { done <- agent.Run(agentCtx, configPath, "s06-real-browser", &agentTestLog{}) }()
	}
	stopAgent := func() error {
		if agentCancel == nil {
			return nil
		}
		cancel, done := agentCancel, agentDone
		agentCancel, agentDone = nil, nil
		cancel()
		select {
		case err := <-done:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case <-time.After(15 * time.Second):
			return errors.New("real S06 Agent did not stop after cancellation")
		}
	}
	startAgent()
	t.Cleanup(func() {
		if err := stopAgent(); err != nil {
			t.Errorf("stop real S06 Agent before removing its owned fixture: %v", err)
		}
	})
	waitForAgentStatusWithin(t, core, config.NodeID, "online", 0, 20*time.Second)
	if err := waitForS06Inventory(coreURL, rootPEM, session, config.NodeID, fixtureIDs, 45*time.Second); err != nil {
		t.Fatalf("real Agent/Core API did not converge on the exact 40-container Engine inventory: %v", err)
	}

	evidenceDir := filepath.Join(root, ".artifacts", "s06", "evidence", "v29", runID)
	if err := os.MkdirAll(filepath.Dir(evidenceDir), 0o700); err != nil {
		t.Fatalf("create S06 evidence parent: %v", err)
	}
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		t.Fatalf("create unique S06 evidence directory: %v", err)
	}
	configFile := filepath.Join(workDir, "browser-config.json")
	browserConfig := s06RealBrowserConfig{URL: coreURL, Session: session, NodeID: config.NodeID,
		ExpectedContainers: fixtureNames, EvidenceDir: evidenceDir}
	encoded, err := json.Marshal(browserConfig)
	if err != nil {
		t.Fatal("encode private S06 browser configuration:", err)
	}
	if err := os.WriteFile(configFile, encoded, 0o600); err != nil {
		t.Fatal("write private S06 browser configuration:", err)
	}
	defer os.Remove(configFile)
	browserResult, err := runS06RealBrowser(t, root, configFile)
	if err != nil {
		t.Fatalf("real embedded browser acceptance process failed: %v", err)
	}

	t.Run("S06-01_fused_overview_caps_at_four_and_detail_has_all_40", func(t *testing.T) {
		result := browserResult.Cases["S06-01"]
		if result.Status != "PASS" || result.FeaturedCount != 4 || result.RenderedCount != s06DINDContainer ||
			result.APIInventoryCount != s06DINDContainer || !result.ExactIDsMatch {
			t.Fatalf("real dashboard inventory result did not pass S06-01: status=%q featured=%d rendered=%d api=%d exact_ids=%t", result.Status, result.FeaturedCount, result.RenderedCount, result.APIInventoryCount, result.ExactIDsMatch)
		}
		t.Logf("S06-01_REAL_BROWSER_PASS baseline=0 engine_owned_containers=40 featured_cards=%d detail_rows=%d api_inventory=%d exact_run_ids=true", result.FeaturedCount, result.RenderedCount, result.APIInventoryCount)
	})

	t.Run("S06-11_embedded_page_has_no_overflow_at_375_768_1440", func(t *testing.T) {
		result := browserResult.Cases["S06-11"]
		if result.Status != "PASS" || !equalS06Strings(result.Viewports, []string{"375", "768", "1440"}) || result.OverflowCount != 0 || result.ControlCount != 3 {
			t.Fatalf("real embedded page responsive result did not pass S06-11: status=%q widths=%v overflows=%d visible_controls=%d", result.Status, result.Viewports, result.OverflowCount, result.ControlCount)
		}
		t.Logf("S06-11_REAL_BROWSER_PASS embedded_core=true widths=375,768,1440 overview_and_docker_detail_checked=true overflows=0 screenshots=%d", len(result.Screenshots))
	})

	t.Run("S06-12_touch_reorder_persists_through_authenticated_core_api", func(t *testing.T) {
		result := browserResult.Cases["S06-12"]
		if result.Status != "PASS" || result.TouchPointerEvents < 3 || result.PreferenceWrites != s06DINDContainer ||
			result.PreferenceWriteFailures != 0 || result.ReloadedFirstID != fixtureIDs[1] {
			t.Fatalf("real touch/API reorder result did not pass S06-12: status=%q touch_pointer_events=%d writes=%d failures=%d first_after_reload_matches_target=%t", result.Status, result.TouchPointerEvents, result.PreferenceWrites, result.PreferenceWriteFailures, result.ReloadedFirstID == fixtureIDs[1])
		}
		preferences := readS06Preferences(t, coreURL, rootPEM, session, config.NodeID)
		if len(preferences) != s06DINDContainer {
			t.Fatalf("authenticated Core preferences API contains %d rows after reorder, want %d", len(preferences), s06DINDContainer)
		}
		byIdentity := make(map[string]dashboard.Preference, len(preferences))
		orders := make(map[int]bool, len(preferences))
		for _, preference := range preferences {
			byIdentity[preference.Identity] = preference
			orders[preference.SortOrder] = true
		}
		source := byIdentity["container:"+fixtureIDs[0]]
		target := byIdentity["container:"+fixtureIDs[1]]
		if source.SortOrder != 1 || target.SortOrder != 0 || !source.Visible || target.Identity == "" || len(orders) != s06DINDContainer {
			t.Fatalf("authenticated preference API did not persist the exact 40-entry reorder: source_order=%d target_order=%d distinct_orders=%d", source.SortOrder, target.SortOrder, len(orders))
		}
		var sqliteRows, sqliteSourceOrder, sqliteTargetOrder int
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM dashboard_preferences WHERE node_id=? AND target_kind='container'`, config.NodeID).Scan(&sqliteRows); err != nil {
			t.Fatal("count real SQLite preferences after touch reorder:", err)
		}
		if err := core.store.DB.QueryRow(`SELECT sort_order FROM dashboard_preferences WHERE node_id=? AND target_kind='container' AND identity_key=?`, config.NodeID, source.Identity).Scan(&sqliteSourceOrder); err != nil {
			t.Fatal("read the exact moved-container preference from SQLite:", err)
		}
		if err := core.store.DB.QueryRow(`SELECT sort_order FROM dashboard_preferences WHERE node_id=? AND target_kind='container' AND identity_key=?`, config.NodeID, target.Identity).Scan(&sqliteTargetOrder); err != nil {
			t.Fatal("read the exact drop-target preference from SQLite:", err)
		}
		if sqliteRows != s06DINDContainer || sqliteSourceOrder != source.SortOrder || sqliteTargetOrder != target.SortOrder {
			t.Fatalf("SQLite rows differ from authenticated API reorder: rows=%d source_order=%d/%d target_order=%d/%d", sqliteRows, sqliteSourceOrder, source.SortOrder, sqliteTargetOrder, target.SortOrder)
		}
		t.Logf("S06-12_REAL_TOUCH_REORDER_PASS pointer_type=touch pointer_events=%d authenticated_puts=%d api_preferences=%d sqlite_preferences=%d source_order=%d target_order=%d reload_persisted=true csrf_and_session=true", result.TouchPointerEvents, result.PreferenceWrites, len(preferences), sqliteRows, source.SortOrder, target.SortOrder)
	})
}

type s06ExpectedContainer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type s06RealBrowserConfig struct {
	URL                string                 `json:"url"`
	Session            string                 `json:"session"`
	NodeID             string                 `json:"nodeId"`
	ExpectedContainers []s06ExpectedContainer `json:"expectedContainers"`
	EvidenceDir        string                 `json:"evidenceDir"`
}

type s06RealBrowserCase struct {
	Status                  string   `json:"status"`
	FeaturedCount           int      `json:"featuredCount"`
	RenderedCount           int      `json:"renderedCount"`
	APIInventoryCount       int      `json:"apiInventoryCount"`
	ExactIDsMatch           bool     `json:"exactIDsMatch"`
	Viewports               []string `json:"viewports"`
	OverflowCount           int      `json:"overflowCount"`
	ControlCount            int      `json:"controlCount"`
	Screenshots             []string `json:"screenshots"`
	TouchPointerEvents      int      `json:"touchPointerEvents"`
	PreferenceWrites        int      `json:"preferenceWrites"`
	PreferenceWriteFailures int      `json:"preferenceWriteFailures"`
	ReloadedFirstID         string   `json:"reloadedFirstID"`
	Error                   string   `json:"error"`
}

type s06RealBrowserResult struct {
	Browser     string                        `json:"browser"`
	Cases       map[string]s06RealBrowserCase `json:"cases"`
	Screenshots []string                      `json:"screenshots"`
}

type s06DINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	NetworkName   string `json:"network_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
	RunID         string `json:"run_id"`
}

func requireOwnedS06DIND(t *testing.T) (root, runRoot, endpoint, runID, engineVersion string) {
	t.Helper()
	endpoint, runID, configuredRoot := os.Getenv(s06DINDHostEnv), os.Getenv(s06DINDRunIDEnv), os.Getenv(s06DINDRootEnv)
	if endpoint == "" && runID == "" && configuredRoot == "" {
		t.Skip("S06 real Engine acceptance requires this run's owner-marked Engine 29 DIND workflow")
	}
	if !strings.HasPrefix(endpoint, "unix://") || runID == "" || configuredRoot == "" {
		t.Fatal("S06 real Engine acceptance requires matching DIND host, run ID, and owner-marked root environment")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal("resolve NodeDance workspace root:", err)
	}
	if !strings.ContainsAny(runID, "/\\") || runID == "." || runID == ".." {
		t.Fatal("S06 DIND run ID is not a single safe path component")
	}
	runRoot, err = filepath.Abs(configuredRoot)
	if err != nil {
		t.Fatal("resolve S06 owner-marked Engine root:", err)
	}
	expectedRunRoot := filepath.Join(root, ".artifacts", "s06", "dind", "v29", runID)
	if runRoot != expectedRunRoot {
		t.Fatalf("refusing an S06 Engine root outside this repository's exact run path: %s", runRoot)
	}
	resolvedRunRoot, err := filepath.EvalSymlinks(runRoot)
	if err != nil || resolvedRunRoot != runRoot {
		t.Fatalf("refusing an S06 Engine run root that is missing or resolves through a symlink: resolved=%q err=%v", resolvedRunRoot, err)
	}
	expectedSocket := filepath.Join(runRoot, "socket", "docker.sock")
	if endpoint != "unix://"+expectedSocket {
		t.Fatalf("refusing Docker endpoint outside this run's owner-marked socket: %s", endpoint)
	}
	socketInfo, err := os.Lstat(expectedSocket)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("refusing S06 Engine path that is not this run's Unix socket: mode=%v err=%v", socketInfo, err)
	}
	markerPath := filepath.Join(runRoot, "owner.json")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		t.Fatalf("S06 DIND owner marker is missing: %v", err)
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 || markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("S06 DIND owner marker is not a private regular file (mode=%#o)", markerInfo.Mode().Perm())
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal("read S06 DIND owner marker:", err)
	}
	var marker s06DINDOwner
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal("decode S06 DIND owner marker:", err)
	}
	lockedImage, err := s06LockedEngineImage(root)
	if err != nil {
		t.Fatal("read locked Engine fixture image:", err)
	}
	if marker.Suite != "nodedance-s06-dind" || marker.RunID != runID || marker.Socket != expectedSocket ||
		marker.ContainerName != "nodedance-s06-dind-v29-"+runID || marker.NetworkName != "nodedance-s06-dind-net-v29-"+runID ||
		marker.Image != lockedImage || marker.HostDaemon != "unix:///var/run/docker.sock" || marker.ServerVersion != "29.7.2" {
		t.Fatal("S06 DIND owner marker does not identify the exact locked Engine, socket, host daemon, and run")
	}
	version, err := s06DockerCLI(endpoint, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		t.Skipf("this run's owner-marked S06 Engine is unavailable: %v", err)
	}
	engineVersion = strings.TrimSpace(version)
	if engineVersion != marker.ServerVersion {
		t.Fatalf("S06 DIND marker/Engine version mismatch: marker=%q actual=%q", marker.ServerVersion, engineVersion)
	}
	return root, runRoot, endpoint, runID, engineVersion
}

func s06LockedEngineImage(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "test-images.lock.json"))
	if err != nil {
		return "", err
	}
	var lock struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return "", err
	}
	image := lock.Images["engine29"]
	if image == "" {
		return "", errors.New("locked Engine 29 image is missing")
	}
	return image, nil
}

func s06DockerCLI(endpoint string, args ...string) (string, error) {
	commandArgs := append([]string{"--host", endpoint}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker command %q on owned S06 Engine failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func s06DockerContainerIDs(endpoint string, filters ...string) ([]string, error) {
	args := []string{"container", "ls", "--all", "--quiet", "--no-trunc"}
	args = append(args, filters...)
	output, err := s06DockerCLI(endpoint, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(output)
	sort.Strings(ids)
	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			return nil, fmt.Errorf("Engine returned duplicate container ID %q", ids[index])
		}
	}
	return ids, nil
}

func s06AuthenticatedRequest(t *testing.T, serverURL string, rootPEM []byte, session, method, path string, body []byte, csrf string) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, serverURL+path, reader)
	if err != nil {
		t.Fatal("create authenticated S06 API request:", err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	request.Header.Set("Origin", serverURL)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set(csrfHeaderName, csrf)
	}
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 10 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("send authenticated S06 API request:", err)
	}
	data, readErr := readLimitedResponse(response, 1<<20)
	if readErr != nil {
		t.Fatal("read authenticated S06 API response:", readErr)
	}
	return response, data
}

func waitForS06Inventory(serverURL string, rootPEM []byte, session, nodeID string, expectedIDs []string, timeout time.Duration) error {
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 5 * time.Second
	path := serverURL + "/api/v1/nodes/" + nodeID + "/containers"
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		response, err := client.Do(request)
		if err == nil {
			data, readErr := readLimitedResponse(response, 8<<20)
			if readErr != nil {
				return readErr
			}
			if response.StatusCode == http.StatusOK {
				var result dashboardDockerMessage
				if decodeErr := json.Unmarshal(data, &result); decodeErr == nil && result.Inventory != nil {
					ids := make([]string, 0, len(result.Inventory.Containers))
					for _, record := range result.Inventory.Containers {
						ids = append(ids, record.Container.ID)
					}
					if result.Inventory.DockerAvailability == "available" && !result.Inventory.DataStale && equalS06IDs(ids, expectedIDs) {
						return nil
					}
					last = fmt.Sprintf("available=%q stale=%t containers=%d ids_match=%t", result.Inventory.DockerAvailability, result.Inventory.DataStale, len(ids), equalS06IDs(ids, expectedIDs))
				}
			} else {
				last = fmt.Sprintf("HTTP %d", response.StatusCode)
			}
		} else {
			last = "Core API request failed"
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Core API did not publish the expected fresh inventory before timeout: %s", last)
}

func readLimitedResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("S06 API response exceeds test limit")
	}
	return data, nil
}

func readS06Preferences(t *testing.T, serverURL string, rootPEM []byte, session, nodeID string) []dashboard.Preference {
	t.Helper()
	response, data := s06AuthenticatedRequest(t, serverURL, rootPEM, session, http.MethodGet, "/api/v1/nodes/"+nodeID+"/preferences", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated Core preferences API returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Preferences []dashboard.Preference `json:"preferences"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal("decode authenticated Core preferences API response:", err)
	}
	return result.Preferences
}

func runS06RealBrowser(t *testing.T, root, configPath string) (s06RealBrowserResult, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		return s06RealBrowserResult{}, fmt.Errorf("locked Node executable unavailable: %w", err)
	}
	script := filepath.Join(root, "web", "tests", "s06-real-browser.mjs")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, script, configPath)
	command.Dir = filepath.Join(root, "web")
	var output bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return s06RealBrowserResult{}, fmt.Errorf("real Chromium harness exceeded its bounded runtime")
		}
		return s06RealBrowserResult{}, fmt.Errorf("real Chromium harness failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	var result s06RealBrowserResult
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &result); err != nil {
		return s06RealBrowserResult{}, fmt.Errorf("decode real Chromium result (output bytes=%d, stderr bytes=%d): %w", output.Len(), stderr.Len(), err)
	}
	if result.Browser == "" || len(result.Cases) != 3 {
		return s06RealBrowserResult{}, fmt.Errorf("real Chromium result omitted its browser identity or exact S06 case results")
	}
	return result, nil
}

func equalS06IDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func equalS06Strings(left, right []string) bool {
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

func shortenedS06IDs(ids []string) []string {
	shortened := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(id) > 12 {
			shortened = append(shortened, id[:12])
		} else {
			shortened = append(shortened, id)
		}
	}
	return shortened
}
