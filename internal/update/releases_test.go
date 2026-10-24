package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The resolver must stay on plain HTTPS downloads: the version file for
// latest-stable discovery and release asset URLs for everything else. No
// GitHub REST API in the update path, so per-IP API quotas cannot block it.
func TestStableComponentResolutionAndArtifactChecksums(t *testing.T) {
	binary := []byte("verified fixture binary")
	sum := sha256.Sum256(binary)
	bad := false
	asset := "nodedance-linux-" + runtime.GOARCH
	versionFile := "core-v0.1.9\n"
	var apiRequested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/core-latest.txt"):
			w.Write([]byte(versionFile))
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			if bad {
				w.Write([]byte("corrupt binary"))
			} else {
				w.Write(binary)
			}
		case strings.Contains(r.URL.Path, "/repos/"):
			apiRequested = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	github := NewGitHub()
	github.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = base.Scheme
		u.Host = base.Host
		copy.URL = &u
		return http.DefaultTransport.RoundTrip(copy)
	})}
	release, err := github.Resolve(context.Background(), "core", "")
	if err != nil || release.Version != "core-v0.1.9" {
		t.Fatalf("stable component resolution: %+v %v", release, err)
	}
	if !strings.HasSuffix(release.BinaryURL, "/releases/download/core-v0.1.9/"+asset) {
		t.Fatalf("binary URL must pin the resolved tag: %q", release.BinaryURL)
	}
	if apiRequested {
		t.Fatal("update resolution must not call the GitHub REST API")
	}
	path := filepath.Join(t.TempDir(), "binary")
	if err := github.Download(context.Background(), release, path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(binary) {
		t.Fatal("artifact changed")
	}
	bad = true
	badPath := filepath.Join(t.TempDir(), "bad")
	if err := github.Download(context.Background(), release, badPath); err == nil {
		t.Fatal("corrupted artifact accepted")
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatal("failed download was not removed")
	}
	if err := github.Download(context.Background(), release, path); err == nil {
		t.Fatal("existing artifact overwritten")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(binary) {
		t.Fatal("existing artifact changed after rejected download")
	}
	if _, err := github.Resolve(context.Background(), "core", "agent-v0.1.9"); err == nil {
		t.Fatal("cross-component release accepted")
	}
	versionFile = "not-a-tag\n"
	if _, err := github.Resolve(context.Background(), "core", ""); err == nil {
		t.Fatal("invalid version file accepted")
	} else if !strings.Contains(err.Error(), "version file") {
		t.Fatalf("unexpected version-file error: %v", err)
	}
}

func TestLargeAgentDownloadAndInstall(t *testing.T) {
	restore := agentWorkerResources()
	defer restore()
	const size = 48 << 20
	hash := sha256.New()
	if _, err := io.CopyN(hash, repeatedBytes('A'), size); err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			fmt.Fprintf(w, "%s  nodedance-agent-linux-%s\n", digest, runtime.GOARCH)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(size))
		_, _ = io.CopyN(w, repeatedBytes('A'), size)
	}))
	defer server.Close()
	resolver := &GitHub{HTTP: server.Client()}
	release := Release{Component: "agent", ChecksumURL: server.URL + "/SHA256SUMS", BinaryURL: server.URL + "/nodedance-agent-linux-" + runtime.GOARCH}
	root := t.TempDir()
	download := filepath.Join(root, "download")
	if err := resolver.Download(context.Background(), release, download); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "previous-binary")
	if err := copyBinary(download, backup); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "installed")
	if err := installBinary(download, installed); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, filepath.Join(data, "state")); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	if err := copyTree(data, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{download, filepath.Join(data, "state"), installed, filepath.Join(snapshot, "state")} {
		if hashFile(t, path) != digest {
			t.Fatalf("artifact changed: %s", path)
		}
	}
}

func TestPublishedNotesUseExactTaggedChangelogWithoutRuntime(t *testing.T) {
	github := NewGitHub()
	requested := []string{}
	notes := "### English\n\n- Published fix.\n\n### 简体中文\n\n- 已发布的修复。"
	changelog := "# Changelog\n\n## [Unreleased]\n\nNot published.\n\n## [0.1.9]\n\n" + notes + "\n\n## [0.1.8]\n\nOlder entry.\n"
	github.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requested = append(requested, r.URL.String())
		body := "core-v0.1.9\n"
		if strings.HasSuffix(r.URL.Path, "/CHANGELOG_CORE.md") {
			body = changelog
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	release, err := github.Release(context.Background(), "core", "")
	if err != nil || release.Notes != notes || release.Version != "core-v0.1.9" {
		t.Fatalf("release notes: %+v %v", release, err)
	}
	if len(requested) != 2 || !strings.Contains(requested[1], "/core-v0.1.9/CHANGELOG_CORE.md") {
		t.Fatalf("wrong changelog source: %v", requested)
	}
	changelog = "## [0.1.8]\n\nold"
	if _, err := github.Release(context.Background(), "core", "core-v0.1.9"); err == nil {
		t.Fatal("missing published notes silently accepted")
	}
}
