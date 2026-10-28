package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestPublicImageETagRevalidation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3, 4}
	if _, err := store.DB.ExecContext(ctx, `UPDATE appearance SET avatar_png=? WHERE id=1`, content); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/public/appearance/avatar", nil)
		if ifNoneMatch != "" {
			request.Header.Set("If-None-Match", ifNoneMatch)
		}
		response := httptest.NewRecorder()
		s.handlePublicImage(response, request)
		return response
	}
	first := get("")
	if first.Code != http.StatusOK {
		t.Fatalf("first GET status %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first GET has no ETag")
	}
	if cacheControl := first.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("Cache-Control %q; want no-cache", cacheControl)
	}
	if first.Body.Len() != len(content) {
		t.Fatalf("first GET body %d bytes; want %d", first.Body.Len(), len(content))
	}
	second := get(etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("revalidation status %d; want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 response carried %d body bytes", second.Body.Len())
	}
	stale := get(`"0000000000000000000000000000000000000000000000000000000000000000"`)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale validator status %d; want 200", stale.Code)
	}
}
