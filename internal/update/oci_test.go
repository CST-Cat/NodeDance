package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentDevOCIResolutionAndDownload(t *testing.T) {
	revision := strings.Repeat("a", 40)
	binaries := map[string][]byte{
		"amd64": []byte("native-amd64-agent"),
		"arm64": []byte("native-arm64-agent"),
	}
	layers := make([]ociDescriptor, 0, 2)
	blobs := map[string][]byte{}
	for _, arch := range []string{"amd64", "arm64"} {
		sum := sha256.Sum256(binaries[arch])
		digest := "sha256:" + hex.EncodeToString(sum[:])
		blobs[digest] = binaries[arch]
		layers = append(layers, ociDescriptor{MediaType: devBinaryMediaType, Digest: digest, Size: int64(len(binaries[arch])), Annotations: map[string]string{"org.opencontainers.image.title": "nodedance-agent-linux-" + arch}})
	}
	binaryManifest, _ := json.Marshal(ociManifest{
		SchemaVersion: 2, MediaType: ociManifestType, ArtifactType: devArtifactType,
		Annotations: map[string]string{"org.opencontainers.image.revision": revision},
		Layers:      layers,
	})
	imageManifest, _ := json.Marshal(ociManifest{
		SchemaVersion: 2, MediaType: ociIndexType,
		Manifests: []ociDescriptor{
			{Digest: "sha256:" + strings.Repeat("b", 64), Size: 900, Platform: &struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			}{OS: "linux", Architecture: "amd64"}},
			{Digest: "sha256:" + strings.Repeat("c", 64), Size: 800, Platform: &struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			}{OS: "linux", Architecture: "arm64"}},
		},
	})
	corrupt := false
	apiCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") || strings.HasPrefix(r.URL.Path, "/actions/") {
			apiCalled = true
			http.Error(w, "no GitHub REST API", 500)
			return
		}
		switch {
		case r.URL.Path == "/token":
			if !strings.Contains(r.URL.Query().Get("scope"), ":pull") {
				http.Error(w, "bad scope", 400)
				return
			}
			w.Write([]byte("{\"token\":\"fixture-token\"}"))
		case strings.Contains(r.URL.Path, "/nodedance-agent-binaries/manifests/"):
			if r.URL.Path != "/v2/"+devBinaryRepository+"/manifests/dev" && r.URL.Path != "/v2/"+devBinaryRepository+"/manifests/dev-"+revision {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", ociManifestType)
			w.Write(binaryManifest)
		case strings.Contains(r.URL.Path, "/nodedance-agent/manifests/dev-"+revision):
			w.Header().Set("Content-Type", ociIndexType)
			w.Write(imageManifest)
		case strings.Contains(r.URL.Path, "/nodedance-agent-binaries/blobs/"):
			digest := strings.TrimPrefix(r.URL.Path, "/v2/"+devBinaryRepository+"/blobs/")
			payload, ok := blobs[digest]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if corrupt {
				payload = []byte("tampered")
			}
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	g := NewGitHub()
	g.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ghcr.io" {
			return nil, fmt.Errorf("unexpected API host %s", r.URL.Host)
		}
		copied := r.Clone(r.Context())
		target := *r.URL
		target.Scheme = base.Scheme
		target.Host = base.Host
		copied.URL = &target
		return http.DefaultTransport.RoundTrip(copied)
	})}
	release, err := g.Resolve(context.Background(), "agent", ChannelDev)
	if err != nil || release.Version != "agent-dev-"+revision || !ociDigest(release.ImageDigest) || !ociDigest(release.BinaryDigest) {
		t.Fatalf("OCI Dev resolve: %+v %v", release, err)
	}
	if apiCalled {
		t.Fatal("GHCR resolution attempted GitHub API")
	}
	m := &Manager{Config: Config{Targets: map[string]Target{"agent": {Mode: "systemd", Binary: "/fixture/nodedance-agent"}}}, Resolver: g,
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("NodeDance Agent agent-v0.1.0\n"), nil
		}}
	check, err := m.Check(context.Background(), Request{Component: "agent", Version: ChannelDev})
	if err != nil || !check.Configured || !check.Available || check.Latest != release.Version || check.Release.BinaryDigest != release.BinaryDigest {
		t.Fatalf("Agent Dev must resolve the published artifact without a source checkout: %+v %v", check, err)
	}
	pinned, err := g.Resolve(context.Background(), "agent", release.Version)
	if err != nil || pinned.BinaryDigest != release.BinaryDigest {
		t.Fatalf("pinned OCI Dev resolve: %+v %v", pinned, err)
	}
	download := filepath.Join(t.TempDir(), "agent")
	if err := g.Download(context.Background(), release, download); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(download)
	if err != nil || string(data) != string(binaries[runtime.GOARCH]) {
		t.Fatalf("wrong native binary: %q %v", data, err)
	}
	corrupt = true
	failed := filepath.Join(t.TempDir(), "bad")
	if err := g.Download(context.Background(), release, failed); err == nil {
		t.Fatal("corrupted OCI blob accepted")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("corrupted OCI download was not removed")
	}
	if err := g.Download(context.Background(), release, download); err == nil {
		t.Fatal("existing binary was overwritten")
	}
}

func TestAgentDevVersionValidation(t *testing.T) {
	revision := strings.Repeat("f", 40)
	for _, candidate := range []string{ChannelDev, "agent-dev-" + revision} {
		if err := Validate(Request{Component: "agent", Version: candidate}); err != nil {
			t.Fatalf("valid Dev version %s: %v", candidate, err)
		}
	}
	for _, candidate := range []string{"agent-dev-short", "core-dev-" + revision, "agent-dev-" + strings.Repeat("g", 40)} {
		if err := Validate(Request{Component: "agent", Version: candidate}); err == nil {
			t.Fatalf("accepted invalid Dev version %s", candidate)
		}
	}
}

func TestOCIManifestRejectsInvalidMetadata(t *testing.T) {
	for _, value := range []string{"sha256:bad", "sha512:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("g", 64)} {
		if ociDigest(value) {
			t.Fatalf("accepted invalid digest %s", value)
		}
	}
	broken := Release{Component: "agent", Version: "agent-dev-" + strings.Repeat("a", 40), BinaryDigest: "sha256:" + strings.Repeat("a", 64), BinarySize: 0}
	g := NewGitHub()
	if err := g.downloadDev(context.Background(), broken, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("accepted zero-length blob")
	}
}
