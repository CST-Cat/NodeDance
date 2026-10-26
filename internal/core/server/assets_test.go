package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func gzippedAsset(t *testing.T, data string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestAssetHandlerCachingAndCompression(t *testing.T) {
	bundle := "console.log('bundle');"
	source := fstest.MapFS{
		"index.html":             {Data: []byte("<html>ok</html>")},
		"assets/index-abc.js":    {Data: []byte(bundle)},
		"assets/index-abc.js.gz": {Data: gzippedAsset(t, bundle)},
		"vendor/theme.css":       {Data: []byte("body{}")},
		"vendor/theme.css.gz":    {Data: gzippedAsset(t, "body{}")},
	}
	handler := newAssetHandler(source)
	get := func(path, encoding string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", path, nil)
		if encoding != "" {
			request.Header.Set("Accept-Encoding", encoding)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	response := get("/assets/index-abc.js", "gzip")
	if response.Code != 200 {
		t.Fatalf("hashed bundle status %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed bundle cache header: %q", got)
	}
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("hashed bundle encoding: %q", got)
	}
	if !stringsContains(response.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("hashed bundle content type: %q", response.Header().Get("Content-Type"))
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("gzipped bundle body unreadable: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || string(decoded) != bundle {
		t.Fatalf("decoded bundle %q, %v", decoded, err)
	}
	response = get("/assets/index-abc.js", "gzip;q=0")
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatal("identity requested but gzip body served")
	}
	if string(response.Body.Bytes()) != bundle {
		t.Fatalf("identity bundle body %q", response.Body.String())
	}
	response = get("/vendor/theme.css", "gzip")
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("unhashed asset cache header: %q", got)
	}
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("unhashed asset missing ETag")
	}
	if !stringsContains(response.Header().Get("Content-Type"), "css") {
		t.Fatalf("unhashed asset content type: %q", response.Header().Get("Content-Type"))
	}
	revalidated := httptest.NewRequest("GET", "/vendor/theme.css", nil)
	revalidated.Header.Set("If-None-Match", etag)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, revalidated)
	if recorder.Code != 304 || recorder.Body.Len() != 0 {
		t.Fatalf("revalidation returned %d with %d bytes", recorder.Code, recorder.Body.Len())
	}
	response = get("/index.html", "gzip")
	if got := response.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index cache header: %q", got)
	}
	if response.Header().Get("ETag") == "" {
		t.Fatal("embedded index missing ETag")
	}
	if get("/assets/index-abc.js.gz", "gzip").Code != 404 {
		t.Fatal("direct .gz requests must 404")
	}
	if get("/assets/missing.js", "gzip").Code != 404 {
		t.Fatal("missing asset must 404")
	}
}

func stringsContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && bytes.Contains([]byte(haystack), []byte(needle))
}
