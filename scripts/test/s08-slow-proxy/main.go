// s08-slow-proxy runs inside the owner-marked DIND container. It forwards a
// Registry v2 endpoint to the pinned local Registry and throttles one blob so
// a real Docker Engine pull can be canceled mid-transfer.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

const statsPath = "/__nodedance_s08/status"

type transferStats struct {
	mu               sync.Mutex
	blobGETs         uint64
	bytesSent        int64
	activeBlobGETs   int64
	canceledBlobGETs uint64
}

type statsSnapshot struct {
	BlobGETs         uint64 `json:"blob_gets"`
	BytesSent        int64  `json:"bytes_sent"`
	ActiveBlobGETs   int64  `json:"active_blob_gets"`
	CanceledBlobGETs uint64 `json:"canceled_blob_gets"`
}

func (s *transferStats) beginBlob() {
	s.mu.Lock()
	s.blobGETs++
	s.activeBlobGETs++
	s.mu.Unlock()
}

func (s *transferStats) addBytes(count int) {
	s.mu.Lock()
	s.bytesSent += int64(count)
	s.mu.Unlock()
}

func (s *transferStats) endBlob(canceled bool) {
	s.mu.Lock()
	s.activeBlobGETs--
	if canceled {
		s.canceledBlobGETs++
	}
	s.mu.Unlock()
}

func (s *transferStats) snapshot() statsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return statsSnapshot{BlobGETs: s.blobGETs, BytesSent: s.bytesSent,
		ActiveBlobGETs: s.activeBlobGETs, CanceledBlobGETs: s.canceledBlobGETs}
}

type throttledBody struct {
	io.ReadCloser
	ctx       context.Context
	rate      int64
	stats     *transferStats
	closeOnce sync.Once
}

func (b *throttledBody) Read(buffer []byte) (int, error) {
	count, readErr := b.ReadCloser.Read(buffer)
	if count == 0 {
		return count, readErr
	}
	delay := time.Duration((int64(count)*int64(time.Second) + b.rate - 1) / b.rate)
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-timer.C:
		}
	}
	b.stats.addBytes(count)
	return count, readErr
}

func (b *throttledBody) Close() error {
	err := b.ReadCloser.Close()
	b.closeOnce.Do(func() {
		b.stats.endBlob(b.ctx.Err() != nil)
	})
	return err
}

func main() {
	listenAddress := flag.String("listen", "0.0.0.0:5002", "DIND-local HTTP listen address")
	upstreamAddress := flag.String("upstream", "http://127.0.0.1:5000", "pinned Registry upstream")
	slowPath := flag.String("slow-path", "", "exact v2 blob path to throttle")
	bytesPerSecond := flag.Int64("bytes-per-second", 2<<20, "maximum bytes per second for the selected blob")
	pidFile := flag.String("pid-file", "/tmp/nodedance-s08-slow-proxy.pid", "process ID file")
	flag.Parse()
	if *slowPath == "" || *bytesPerSecond < 1 {
		fatal("slow-path and a positive bytes-per-second value are required")
	}
	upstream, err := url.Parse(*upstreamAddress)
	if err != nil || upstream.Scheme != "http" || upstream.Host == "" {
		fatal("upstream must be an HTTP Registry URL")
	}
	if err := os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		fatal("could not write process ID file")
	}

	stats := &transferStats{}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ModifyResponse = func(response *http.Response) error {
		request := response.Request
		if request != nil && request.Method == http.MethodGet && request.URL.Path == *slowPath && response.StatusCode == http.StatusOK {
			stats.beginBlob()
			response.Body = &throttledBody{ReadCloser: response.Body, ctx: request.Context(), rate: *bytesPerSecond, stats: stats}
		}
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc(statsPath, func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(stats.snapshot())
	})
	mux.Handle("/", proxy)
	server := &http.Server{Addr: *listenAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal("HTTP server stopped unexpectedly")
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "s08-slow-proxy:", message)
	os.Exit(2)
}
