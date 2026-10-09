package images

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"golang.org/x/crypto/bcrypt"
)

type s08DINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
	RunID         string `json:"run_id"`
}

type s08LockedImages struct {
	Images map[string]string `json:"images"`
}

// TestDINDImageManagementRealEngine exercises the image SDK and durable
// executor against a run-owned Docker Engine and local, pinned Registry.
// It never controls the host Docker daemon. Set NODEDANCE_S08_DIND_ROOT and
// NODEDANCE_S08_RUN_ID using scripts/test/s08-dind.sh before running it.
func TestDINDImageManagementRealEngine(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("NODEDANCE_S08_DIND_ROOT"))
	runID := strings.TrimSpace(os.Getenv("NODEDANCE_S08_RUN_ID"))
	if root == "" || runID == "" {
		t.Skip("NOT_READY: set NODEDANCE_S08_DIND_ROOT and NODEDANCE_S08_RUN_ID to an owned S08 DIND fixture")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,35}$`).MatchString(runID) {
		t.Fatalf("invalid S08 run ID %q", runID)
	}
	repoRoot, err := findS08RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	engineVersion := "28"
	if strings.Contains(filepath.ToSlash(root), "/v29/") {
		engineVersion = "29"
	}
	expectedRoot := filepath.Join(repoRoot, ".artifacts", "s08", "dind", "v"+engineVersion, runID)
	if filepath.Clean(root) != filepath.Clean(expectedRoot) {
		t.Fatalf("test Engine root must be this checkout's S08 run root %q, got %q", expectedRoot, root)
	}
	markerBytes, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		t.Fatalf("S08 owner marker is required: %v", err)
	}
	var owner s08DINDOwner
	if err := json.Unmarshal(markerBytes, &owner); err != nil {
		t.Fatalf("decode S08 owner marker: %v", err)
	}
	socket := filepath.Join(root, "socket", "docker.sock")
	if owner.Suite != "nodedance-s08-dind" || owner.RunID != runID || filepath.Clean(owner.Socket) != socket ||
		!strings.HasPrefix(owner.ServerVersion, engineVersion+".") || owner.ContainerName == "" || owner.Image == "" || owner.HostDaemon == "" {
		t.Fatalf("S08 owner marker does not identify this dedicated Engine: %+v", owner)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("dedicated S08 Engine socket is unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cliConfig := t.TempDir()
	if err := os.Chmod(cliConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	docker := func(args ...string) (string, error) {
		return s08DockerCommand(ctx, "unix://"+socket, cliConfig, args...)
	}
	version, err := docker("version", "--format", "{{.Server.Version}}")
	if err != nil || !strings.HasPrefix(version, engineVersion+".") || version != owner.ServerVersion {
		t.Fatalf("Engine version does not match owner marker: got %q, owner=%q, err=%v", version, owner.ServerVersion, err)
	}
	architecture, err := docker("version", "--format", "{{.Server.Arch}}")
	if err != nil || architecture != "amd64" && architecture != "arm64" {
		t.Fatalf("real image fixture supports the test Engine amd64/arm64 architectures, got %q err=%v", architecture, err)
	}
	var locked s08LockedImages
	lockBytes, err := os.ReadFile(filepath.Join(repoRoot, "test-images.lock.json"))
	if err != nil || json.Unmarshal(lockBytes, &locked) != nil {
		t.Fatalf("read locked test images: %v", err)
	}
	registryImage := locked.Images["registry"]
	if registryImage == "" || !strings.Contains(registryImage, "@sha256:") {
		t.Fatal("S08 Registry image must be pinned by digest")
	}
	engine, err := NewSDKEngine("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close real image SDK Engine: %v", err)
		}
	}()
	if _, err := engine.List(ctx); err != nil {
		t.Fatalf("list dedicated Docker Engine before fixture setup: %v", err)
	}

	suite := "nodedance-s08-image-" + runID
	registryName := "nd-s08-registry-" + shortS08RunID(runID)
	if _, err := docker("run", "--detach", "--name", registryName, "--network", "host",
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+suite,
		"--env", "REGISTRY_HTTP_ADDR=0.0.0.0:5000", registryImage); err != nil {
		t.Fatalf("start pinned local Registry in the dedicated Engine: %v", err)
	}
	t.Cleanup(func() {
		labels, inspectErr := s08DockerCommand(context.Background(), "unix://"+socket, cliConfig, "inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, registryName)
		if inspectErr == nil && strings.TrimSpace(labels) == suite {
			if _, removeErr := s08DockerCommand(context.Background(), "unix://"+socket, cliConfig, "rm", "--force", registryName); removeErr != nil {
				t.Errorf("remove owned S08 Registry fixture: %v", removeErr)
			}
		} else if inspectErr != nil && !strings.Contains(strings.ToLower(inspectErr.Error()), "no such object") {
			t.Errorf("verify owned S08 Registry before cleanup: %v", inspectErr)
		}
	})
	outerConfig := t.TempDir()
	if err := os.Chmod(outerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	containerIPs, err := s08DockerCommand(ctx, owner.HostDaemon, outerConfig, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", owner.ContainerName)
	if err != nil {
		t.Fatalf("read only this S08 DIND container's bridge address: %v", err)
	}
	fields := strings.Fields(containerIPs)
	if len(fields) == 0 || net.ParseIP(fields[0]) == nil {
		t.Fatalf("S08 DIND owner has no usable bridge address: %q", containerIPs)
	}
	registryHost := net.JoinHostPort(fields[0], "5000")
	if err := waitS08RegistryHTTP(ctx, "http://"+registryHost); err != nil {
		t.Fatalf("local Registry is not reachable over this run's private DIND bridge: %v", err)
	}

	fixture, err := buildS08ImageFixture(architecture)
	if err != nil {
		t.Fatal(err)
	}
	registryRepo := "nodedance/s08-fixture"
	registryTag := "fixture-" + shortS08RunID(runID)
	registryRef := "127.0.0.1:5000/" + registryRepo + ":" + registryTag
	expectedRepoDigest, err := pushS08ImageFixture(ctx, "http://"+registryHost, registryRepo, registryTag, fixture)
	if err != nil {
		t.Fatalf("upload generated fixture directly to the local Registry: %v", err)
	}
	if expectedRepoDigest != "127.0.0.1:5000/"+registryRepo+"@"+fixture.ManifestDigest {
		t.Fatalf("Registry returned repository digest %q, locally generated manifest digest is %q", expectedRepoDigest, fixture.ManifestDigest)
	}
	if _, err := engine.Inspect(ctx, registryRef); !errdefs.IsNotFound(err) {
		t.Fatalf("pre-pull inspection must show the fixture absent from Docker Engine, got err=%v", err)
	}

	journal, journalPath := openImageJournalWithPath(t)
	executor, err := NewExecutor(engine, journal)
	if err != nil {
		t.Fatal(err)
	}
	pullIntent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + registryRef), ImageReference: registryRef}
	pullDispatch := enqueueImageDispatch(t, journal, pullIntent, "s08-pull-"+shortS08RunID(runID), nil)
	var observedTransferProgress bool
	pulledTask, err := executor.ExecuteImage(ctx, pullDispatch, func() {
		latest, readErr := journal.Get(context.Background(), pullDispatch.TaskID)
		if readErr == nil && latest.Progress.Total > 0 {
			observedTransferProgress = true
		}
	})
	if err != nil || pulledTask.Status != taskstate.Succeeded || !pulledTask.Evidence.PostconditionVerified {
		t.Fatalf("real Registry image pull must verify Engine completion: task=%+v err=%v", pulledTask, err)
	}
	t.Logf("real Engine pull progress snapshot: completed=%d total=%d; persisted progress callback observed=%t",
		pulledTask.Progress.Completed, pulledTask.Progress.Total, observedTransferProgress)
	pulled, err := engine.Inspect(ctx, registryRef)
	if err != nil || pulled.ID != fixture.ConfigDigest || !hasS08String(pulled.Digests, expectedRepoDigest) {
		t.Fatalf("pulled image ID/digest does not match the Registry manifest: image=%+v expectedID=%q expectedDigest=%q err=%v", pulled, fixture.ConfigDigest, expectedRepoDigest, err)
	}

	localTagA := "nodedance-s08/multitag-a:" + shortS08RunID(runID)
	localTagB := "nodedance-s08/multitag-b:" + shortS08RunID(runID)
	if _, err := docker("image", "tag", pulled.ID, localTagA); err != nil {
		t.Fatalf("add first local tag to pulled image: %v", err)
	}
	if _, err := docker("image", "tag", pulled.ID, localTagB); err != nil {
		t.Fatalf("add second local tag to pulled image: %v", err)
	}
	listed, ok := findS08ImageMust(t, engine, ctx, pulled.ID)
	if !ok || !hasS08String(listed.Tags, registryRef) || !hasS08String(listed.Tags, localTagA) || !hasS08String(listed.Tags, localTagB) ||
		!hasS08String(listed.Digests, expectedRepoDigest) || listed.Containers != 0 {
		t.Fatalf("real Engine list did not retain multi-tag/digest identity: %+v", listed)
	}

	containerName := "nd-s08-ref-" + shortS08RunID(runID)
	containerID, err := docker("create", "--name", containerName,
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+suite,
		pulled.ID)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(strings.TrimSpace(containerID)) {
		t.Fatalf("create stopped image-reference fixture: id=%q err=%v", containerID, err)
	}
	imageWithRef, err := engine.Inspect(ctx, pulled.ID)
	if err != nil || imageWithRef.Containers != 1 {
		t.Fatalf("real Engine image reference count = %d, err=%v; want stopped container included", imageWithRef.Containers, err)
	}
	listedWithRef, ok := findS08ImageMust(t, engine, ctx, pulled.ID)
	if !ok || listedWithRef.Containers != 1 {
		t.Fatalf("real Engine image list did not count the stopped container reference: %+v", listedWithRef)
	}
	deleteIntent := protocol.TaskIntent{Action: protocol.TaskImageDelete, ContainerID: protocol.ImageTargetKey("delete:" + pulled.ID), ImageID: pulled.ID}
	inUseDispatch := enqueueImageDispatch(t, journal, deleteIntent, "s08-delete-used-"+shortS08RunID(runID), nil)
	inUseTask, err := executor.ExecuteImage(ctx, inUseDispatch, nil)
	if err != nil || inUseTask.Status != taskstate.Failed || inUseTask.Result.ObservedState != "in_use" {
		t.Fatalf("delete of an image used by a stopped container was not confirmed refused: task=%+v err=%v", inUseTask, err)
	}
	if _, err := engine.Inspect(ctx, pulled.ID); err != nil {
		t.Fatalf("in-use delete removed the image: %v", err)
	}
	label, err := docker("inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, strings.TrimSpace(containerID))
	if err != nil || strings.TrimSpace(label) != suite {
		t.Fatalf("refuse to remove a container not owned by this S08 test: label=%q err=%v", label, err)
	}
	if _, err := docker("rm", strings.TrimSpace(containerID)); err != nil {
		t.Fatalf("remove only the owned stopped reference fixture: %v", err)
	}
	imageWithRef, err = engine.Inspect(ctx, pulled.ID)
	if err != nil || imageWithRef.Containers != 0 {
		t.Fatalf("real Engine reference count after removing the owned container = %+v, err=%v", imageWithRef, err)
	}
	if _, err := docker("image", "rm", localTagA, localTagB); err != nil {
		t.Fatalf("remove only the two test-created local tags before the safe delete: %v", err)
	}
	unusedDispatch := enqueueImageDispatch(t, journal, deleteIntent, "s08-delete-unused-"+shortS08RunID(runID), nil)
	unusedTask, err := executor.ExecuteImage(ctx, unusedDispatch, nil)
	if err != nil || unusedTask.Status != taskstate.Succeeded || unusedTask.Result.ObservedState != "absent" {
		t.Fatalf("delete of the now-unreferenced image was not verified: task=%+v err=%v", unusedTask, err)
	}
	if _, err := engine.Inspect(ctx, pulled.ID); !errdefs.IsNotFound(err) {
		t.Fatalf("successfully deleted image is still present: %v", err)
	}

	missingRef := "127.0.0.1:5000/nodedance/s08-missing:absent-" + shortS08RunID(runID)
	missingIntent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + missingRef), ImageReference: missingRef}
	missingDispatch := enqueueImageDispatch(t, journal, missingIntent, "s08-pull-missing-"+shortS08RunID(runID), nil)
	missingTask, err := executor.ExecuteImage(ctx, missingDispatch, nil)
	if err != nil || missingTask.Status != taskstate.Failed || missingTask.Result.ObservedState != "missing_after_pull" {
		t.Fatalf("confirmed missing Registry manifest must fail rather than succeed or remain ambiguous: task=%+v err=%v", missingTask, err)
	}
	if _, err := engine.Inspect(ctx, missingRef); !errdefs.IsNotFound(err) {
		t.Fatalf("missing Registry reference is unexpectedly present according to Docker SDK: err=%v", err)
	}
	if _, err := docker("image", "inspect", missingRef); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "no such image") {
		t.Fatalf("missing Registry reference is unexpectedly present according to Docker CLI: err=%v", err)
	}
	if err := engine.Pull(ctx, "not a valid image reference", "", nil); err == nil {
		t.Fatal("invalid image reference was accepted by the real SDK Engine adapter")
	}

	t.Run("private Registry authentication is ephemeral", func(t *testing.T) {
		username := os.Getenv("NODEDANCE_S08_REGISTRY_USER")
		password := os.Getenv("NODEDANCE_S08_REGISTRY_PASSWORD")
		if username == "" || password == "" {
			t.Fatal("S08 auth canaries must be supplied by the acceptance runner")
		}
		if len(username) > 256 || len(password) > 4096 || strings.ContainsAny(username+password, "\x00\r\n\x7f") {
			t.Fatal("S08 auth canaries are invalid")
		}
		wrongPassword := password + "-wrong"
		if strings.Contains(username, ":") {
			t.Fatal("S08 auth canary username cannot contain a colon")
		}
		privateRegistryName := "nd-s08-private-registry-" + shortS08RunID(runID)
		privateRegistryHost := net.JoinHostPort(fields[0], "5001")
		privateRegistryBase := "http://" + privateRegistryHost
		privateRegistryRef := "127.0.0.1:5001/nodedance/s08-private:fixture-" + shortS08RunID(runID)

		t.Cleanup(func() {
			labels, inspectErr := s08DockerCommand(context.Background(), "unix://"+socket, cliConfig,
				"inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, privateRegistryName)
			if inspectErr == nil && strings.TrimSpace(labels) == suite {
				if _, removeErr := s08DockerCommand(context.Background(), "unix://"+socket, cliConfig, "rm", "--force", privateRegistryName); removeErr != nil {
					t.Errorf("remove owned S08 private Registry fixture: %v", removeErr)
				}
			} else if inspectErr != nil && !strings.Contains(strings.ToLower(inspectErr.Error()), "no such object") {
				t.Errorf("verify owned S08 private Registry before cleanup: %v", inspectErr)
			}
		})
		if err := startS08PrivateRegistry(ctx, docker, privateRegistryName, suite, registryImage, username, password); err != nil {
			t.Fatalf("start owned private Registry fixture: %v", err)
		}
		if err := waitS08PrivateRegistry(ctx, privateRegistryBase, username, password); err != nil {
			t.Fatalf("private Registry did not accept the valid fixture credential: %v", err)
		}
		privateDigest, err := pushS08ImageFixtureAuth(ctx, privateRegistryBase, "nodedance/s08-private",
			"fixture-"+shortS08RunID(runID), fixture, username, password)
		if err != nil {
			t.Fatalf("seed private Registry through authenticated v2 API: %v", err)
		}
		if privateDigest != "127.0.0.1:5001/nodedance/s08-private@"+fixture.ManifestDigest {
			t.Fatal("private Registry returned an unexpected manifest digest")
		}
		if _, err := engine.Inspect(ctx, privateRegistryRef); !errdefs.IsNotFound(err) {
			t.Fatalf("private fixture must be absent before Agent pull, err=%v", err)
		}

		privateIntent := protocol.TaskIntent{Action: protocol.TaskImagePull,
			ContainerID: protocol.ImageTargetKey("pull:" + privateRegistryRef), ImageReference: privateRegistryRef}
		badDispatch := enqueueImageDispatch(t, journal, privateIntent, "s08-private-denied-"+shortS08RunID(runID),
			&protocol.RegistryCredentials{Username: username, Password: wrongPassword})
		badTask, err := executor.ExecuteImage(ctx, badDispatch, nil)
		if err != nil || badTask.Status != taskstate.Failed || badTask.Result.ObservedState != "registry_auth_failed" {
			t.Fatalf("invalid private Registry credentials were not confirmed rejected: status=%s code=%s state=%s err=%v",
				badTask.Status, badTask.Result.Code, badTask.Result.ObservedState, err)
		}
		if badDispatch.RegistryAuth == nil || badDispatch.RegistryAuth.Username != "" || badDispatch.RegistryAuth.Password != "" {
			t.Fatal("rejected credential fields remained in the in-memory Agent dispatch")
		}
		if _, err := engine.Inspect(ctx, privateRegistryRef); !errdefs.IsNotFound(err) {
			t.Fatalf("rejected authentication unexpectedly installed the image, err=%v", err)
		}

		goodDispatch := enqueueImageDispatch(t, journal, privateIntent, "s08-private-accepted-"+shortS08RunID(runID),
			&protocol.RegistryCredentials{Username: username, Password: password})
		goodTask, err := executor.ExecuteImage(ctx, goodDispatch, nil)
		if err != nil || goodTask.Status != taskstate.Succeeded || !goodTask.Evidence.PostconditionVerified {
			t.Fatalf("valid private Registry credentials did not complete a verified Agent pull: status=%s state=%s err=%v",
				goodTask.Status, goodTask.Result.ObservedState, err)
		}
		if goodDispatch.RegistryAuth == nil || goodDispatch.RegistryAuth.Username != "" || goodDispatch.RegistryAuth.Password != "" {
			t.Fatal("accepted credential fields remained in the in-memory Agent dispatch")
		}
		privateImage, err := engine.Inspect(ctx, privateRegistryRef)
		if err != nil || privateImage.ID != fixture.ConfigDigest || !hasS08String(privateImage.Digests, privateDigest) {
			t.Fatal("authenticated Agent pull did not install the expected private Registry image")
		}

		for name, result := range map[string]taskjournal.Snapshot{"rejected": badTask, "accepted": goodTask} {
			serialized, marshalErr := json.Marshal(result)
			if marshalErr != nil || bytes.Contains(serialized, []byte(username)) || bytes.Contains(serialized, []byte(password)) || bytes.Contains(serialized, []byte(wrongPassword)) {
				t.Fatalf("%s pull result contains Registry credentials", name)
			}
		}
		if err := assertS08JournalOmitsCredentials(journalPath, username, password, wrongPassword); err != nil {
			t.Fatal(err)
		}
		t.Log("private Registry accepted and rejected credentials through the real Agent executor; task result and durable journal contain no credential canaries")
	})
}

type s08Fixture struct {
	ConfigBytes    []byte
	LayerBytes     []byte
	ManifestBytes  []byte
	ConfigDigest   string
	ManifestDigest string
}

func buildS08ImageFixture(architecture string) (s08Fixture, error) {
	payload := make([]byte, 2<<20)
	for offset, counter := 0, uint64(0); offset < len(payload); counter++ {
		block := sha256.Sum256([]byte(fmt.Sprintf("nodedance-s08-image-fixture-%08x", counter)))
		offset += copy(payload[offset:], block[:])
	}
	var uncompressed bytes.Buffer
	tarWriter := tar.NewWriter(&uncompressed)
	header := &tar.Header{Name: "nodedance/s08-fixture.bin", Mode: 0o644, Size: int64(len(payload)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Format: tar.FormatUSTAR}
	if err := tarWriter.WriteHeader(header); err != nil {
		return s08Fixture{}, err
	}
	if _, err := tarWriter.Write(payload); err != nil {
		return s08Fixture{}, err
	}
	if err := tarWriter.Close(); err != nil {
		return s08Fixture{}, err
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	if _, err := gzipWriter.Write(uncompressed.Bytes()); err != nil {
		return s08Fixture{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		return s08Fixture{}, err
	}
	diffID := s08Digest(uncompressed.Bytes())
	config := struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Config       struct {
			Cmd    []string          `json:"Cmd,omitempty"`
			Labels map[string]string `json:"Labels,omitempty"`
		} `json:"config"`
		RootFS struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}{Architecture: architecture, OS: "linux"}
	config.Config.Cmd = []string{"/bin/sh"}
	config.Config.Labels = map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": "nodedance-s08-fixture"}
	config.RootFS.Type = "layers"
	config.RootFS.DiffIDs = []string{diffID}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return s08Fixture{}, err
	}
	type descriptor struct {
		MediaType string `json:"mediaType"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	}
	manifest := struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}{SchemaVersion: 2, MediaType: "application/vnd.docker.distribution.manifest.v2+json"}
	manifest.Config = descriptor{MediaType: "application/vnd.docker.container.image.v1+json", Size: len(configBytes), Digest: s08Digest(configBytes)}
	manifest.Layers = []descriptor{{MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip", Size: compressed.Len(), Digest: s08Digest(compressed.Bytes())}}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return s08Fixture{}, err
	}
	return s08Fixture{ConfigBytes: configBytes, LayerBytes: compressed.Bytes(), ManifestBytes: manifestBytes,
		ConfigDigest: manifest.Config.Digest, ManifestDigest: s08Digest(manifestBytes)}, nil
}

func pushS08ImageFixture(ctx context.Context, registryBase, repository, tag string, fixture s08Fixture) (string, error) {
	return pushS08ImageFixtureAuth(ctx, registryBase, repository, tag, fixture, "", "")
}

func pushS08ImageFixtureAuth(ctx context.Context, registryBase, repository, tag string, fixture s08Fixture, username, password string) (string, error) {
	for digest, data := range map[string][]byte{fixture.ConfigDigest: fixture.ConfigBytes,
		s08Digest(fixture.LayerBytes): fixture.LayerBytes} {
		if err := s08RegistryUploadBlobAuth(ctx, registryBase, repository, digest, data, username, password); err != nil {
			return "", err
		}
	}
	manifestURL := registryBase + "/v2/" + repository + "/manifests/" + url.PathEscape(tag)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, manifestURL, bytes.NewReader(fixture.ManifestBytes))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("Registry manifest upload returned HTTP %d", response.StatusCode)
	}
	digest := response.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", errors.New("Registry manifest upload omitted Docker-Content-Digest")
	}
	base, err := url.Parse(registryBase)
	if err != nil {
		return "", errors.New("invalid S08 Registry fixture URL")
	}
	_, port, err := net.SplitHostPort(base.Host)
	if err != nil || port == "" {
		return "", errors.New("S08 Registry fixture URL omitted its port")
	}
	return net.JoinHostPort("127.0.0.1", port) + "/" + repository + "@" + digest, nil
}

func s08RegistryUploadBlob(ctx context.Context, registryBase, repository, digest string, data []byte) error {
	return s08RegistryUploadBlobAuth(ctx, registryBase, repository, digest, data, "", "")
}

func s08RegistryUploadBlobAuth(ctx context.Context, registryBase, repository, digest string, data []byte, username, password string) error {
	startURL := registryBase + "/v2/" + repository + "/blobs/uploads/"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, nil)
	if err != nil {
		return err
	}
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Registry blob upload initialization returned HTTP %d", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if location == "" {
		return errors.New("Registry blob upload omitted its upload location")
	}
	base, err := url.Parse(startURL)
	if err != nil {
		return err
	}
	uploadURL, err := base.Parse(location)
	if err != nil {
		return err
	}
	query := uploadURL.Query()
	query.Set("digest", digest)
	uploadURL.RawQuery = query.Encode()
	request, err = http.NewRequestWithContext(ctx, http.MethodPut, uploadURL.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("Registry blob upload returned HTTP %d", response.StatusCode)
	}
	return nil
}

func waitS08RegistryHTTP(ctx context.Context, base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("registry /v2/ endpoint did not return HTTP 200 at %s", base)
}

func startS08PrivateRegistry(ctx context.Context, docker func(...string) (string, error), name, suite, image, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("could not hash S08 Registry fixture credential")
	}
	root, err := os.MkdirTemp("", "nodedance-s08-registry-auth-")
	if err != nil {
		return errors.New("could not create S08 private Registry auth fixture")
	}
	defer os.RemoveAll(root)
	htpasswdPath := filepath.Join(root, "htpasswd")
	if err := os.WriteFile(htpasswdPath, append(append([]byte(username+":"), hash...), '\n'), 0o600); err != nil {
		return errors.New("could not write S08 private Registry auth fixture")
	}
	containerID, err := docker("create", "--name", name, "--network", "host",
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+suite,
		"--env", "REGISTRY_HTTP_ADDR=0.0.0.0:5001",
		"--env", "REGISTRY_AUTH=htpasswd",
		"--env", "REGISTRY_AUTH_HTPASSWD_REALM=NodeDance S08 private Registry",
		"--env", "REGISTRY_AUTH_HTPASSWD_PATH=/etc/docker/registry/htpasswd",
		image)
	if err != nil || strings.TrimSpace(containerID) == "" {
		return errors.New("could not create S08 private Registry container")
	}
	if _, err := docker("cp", htpasswdPath, name+":/etc/docker/registry/htpasswd"); err != nil {
		return errors.New("could not install S08 private Registry credential verifier")
	}
	if _, err := docker("start", name); err != nil {
		return errors.New("could not start S08 private Registry container")
	}
	return nil
}

func waitS08PrivateRegistry(ctx context.Context, base, username, password string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s08RegistryStatus(ctx, client, base, username, password)
		if err == nil && status == http.StatusOK {
			anonymousStatus, anonymousErr := s08RegistryStatus(ctx, client, base, "", "")
			if anonymousErr == nil && anonymousStatus == http.StatusUnauthorized {
				return nil
			}
			return errors.New("private Registry did not reject anonymous access with HTTP 401")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("private Registry did not become ready with the configured credential")
}

func s08RegistryStatus(ctx context.Context, client *http.Client, base, username, password string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		return 0, err
	}
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func assertS08JournalOmitsCredentials(path, username, password, wrongPassword string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("could not inspect durable Agent image journal for auth canaries")
		}
		if bytes.Contains(data, []byte(username)) || bytes.Contains(data, []byte(password)) || bytes.Contains(data, []byte(wrongPassword)) {
			return errors.New("durable Agent image journal contains Registry auth canaries")
		}
	}
	return nil
}

func s08Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func findS08RepositoryRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for directory := cwd; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("could not find repository go.mod")
		}
	}
}

func s08DockerCommand(ctx context.Context, host, config string, args ...string) (string, error) {
	cliArgs := append([]string{"--host", host}, args...)
	command := exec.CommandContext(ctx, "docker", cliArgs...)
	command.Env = append(os.Environ(), "DOCKER_CONFIG="+config)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		return strings.TrimSpace(stdout.String()), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, combined)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func shortS08RunID(value string) string {
	clean := strings.ToLower(regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(value, ""))
	if len(clean) > 20 {
		clean = clean[:20]
	}
	if clean == "" {
		return "local"
	}
	return clean
}

func findS08Image(images []Image, id string) (Image, bool) {
	for _, image := range images {
		if image.ID == id {
			return image, true
		}
	}
	return Image{}, false
}

func findS08ImageMust(t *testing.T, engine *SDKEngine, ctx context.Context, id string) (Image, bool) {
	t.Helper()
	images, err := engine.List(ctx)
	if err != nil {
		t.Fatalf("list real Engine images: %v", err)
	}
	image, ok := findS08Image(images, id)
	return image, ok
}

func hasS08String(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
