package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	if err := github.Download(context.Background(), release, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("corrupted artifact accepted")
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
