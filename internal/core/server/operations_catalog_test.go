package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationCatalogETagRevalidation(t *testing.T) {
	s := featureCore(t)
	host := httptest.NewServer(s)
	defer host.Close()

	response, err := http.Get(host.URL + "/api/v1/operations")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("catalog status %d", response.StatusCode)
	}
	etag := response.Header.Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, "\"") {
		t.Fatalf("catalog ETag %q", etag)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("catalog cache control %q", got)
	}
	var catalog struct {
		Version    int               `json:"version"`
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Operations) == 0 {
		t.Fatal("catalog has no operations")
	}

	request, err := http.NewRequest(http.MethodGet, host.URL+"/api/v1/operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-None-Match", etag)
	revalidated, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	revalidatedBody, err := io.ReadAll(revalidated.Body)
	revalidated.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation status %d", revalidated.StatusCode)
	}
	if len(revalidatedBody) != 0 {
		t.Fatalf("304 carried %d body bytes", len(revalidatedBody))
	}
	if got := revalidated.Header.Get("ETag"); got != etag {
		t.Fatalf("304 ETag %q, want %q", got, etag)
	}
	request.Header.Set("If-None-Match", "W/"+etag)
	weak, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	weak.Body.Close()
	if weak.StatusCode != http.StatusNotModified {
		t.Fatalf("weak etag status %d", weak.StatusCode)
	}

	stale, err := http.NewRequest(http.MethodGet, host.URL+"/api/v1/operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	stale.Header.Set("If-None-Match", "\"stale\"")
	fresh, err := http.DefaultClient.Do(stale)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Body.Close()
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("stale etag status %d", fresh.StatusCode)
	}
}
