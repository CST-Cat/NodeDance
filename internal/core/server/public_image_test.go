package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func encodeTestImage(t *testing.T, width, height int, asJPEG bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				uint8(x * 255 / max(width-1, 1)),
				uint8(y * 255 / max(height-1, 1)),
				uint8((x + y) * 255 / max(width+height-2, 1)),
				255,
			})
		}
	}
	var buffer bytes.Buffer
	var err error
	if asJPEG {
		err = jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&buffer, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestBackgroundImageAcceptsWallpaperSizes(t *testing.T) {
	normalized, err := normalizeBackgroundImage(encodeTestImage(t, 1920, 1080, true))
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(normalized))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || config.Width != 1920 || config.Height != 1080 {
		t.Fatalf("stored %s %dx%d; want jpeg 1920x1080", format, config.Width, config.Height)
	}
	if _, err := normalizeRasterImage(encodeTestImage(t, 1920, 1080, true)); err == nil {
		t.Fatal("avatar normalization still accepts wallpaper sizes")
	}
}

func TestBackgroundImageDownscalesToStoredBox(t *testing.T) {
	normalized, err := normalizeBackgroundImage(encodeTestImage(t, 5000, 3000, false))
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(normalized))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" {
		t.Fatalf("stored format %s; want png", format)
	}
	if config.Width > maxBackgroundStoredWidth || config.Height > maxBackgroundStoredHeight {
		t.Fatalf("stored %dx%d exceeds box %dx%d", config.Width, config.Height, maxBackgroundStoredWidth, maxBackgroundStoredHeight)
	}
	if config.Width != 3600 || config.Height != 2160 {
		t.Fatalf("stored %dx%d; want proportional 3600x2160", config.Width, config.Height)
	}
}

func TestBackgroundImageRejectsBeyondAdmission(t *testing.T) {
	if _, err := normalizeBackgroundImage(encodeTestImage(t, 8192, 5000, true)); err == nil {
		t.Fatal("background normalization accepts more than the admission pixel cap")
	}
}

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
