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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStableComponentResolutionAndArtifactChecksums(t *testing.T) {
	binary := []byte("verified fixture binary")
	sum := sha256.Sum256(binary)
	bad := false
	asset := "nodedance-linux-" + runtime.GOARCH
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases?") || strings.HasSuffix(r.URL.Path, "/releases"):
			releases := []map[string]any{}
			for _, tag := range []string{"agent-v99.0.0", "core-v0.1.8", "core-v0.1.9", "core-v0.2.0"} {
				assets := []map[string]string{}
				for _, name := range []string{"nodedance-update.json", "SHA256SUMS", asset} {
					assets = append(assets, map[string]string{"name": name, "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name})
				}
				releases = append(releases, map[string]any{"tag_name": tag, "prerelease": tag == "core-v0.2.0", "assets": assets})
			}
			json.NewEncoder(w).Encode(releases)
		case strings.HasSuffix(r.URL.Path, "/nodedance-update.json"):
			json.NewEncoder(w).Encode(Describe("core", "core-v0.1.9"))
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			if bad {
				w.Write([]byte("corrupt binary"))
			} else {
				w.Write(binary)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	github := NewGitHub()
	github.API = server.URL
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
}
