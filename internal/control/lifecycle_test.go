package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentLoginAndReadKeepCookieJarStable(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/csrf" {
			http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
			io.WriteString(w, `{"token":"csrf"}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer host.Close()
	client, err := NewClient(ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	original := client.http.Jar
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, login := range []bool{false, true} {
		wg.Add(1)
		go func(login bool) {
			defer wg.Done()
			<-start
			o := Operation{Name: "read", Method: "GET", Path: "/api/v1/read", Transport: "json"}
			if login {
				o.Name = "auth.login"
				o.Method = "POST"
				o.Path = "/api/v1/auth/login"
			}
			for range 50 {
				if result := client.Execute(context.Background(), o, Input{}, nil, nil); result.Error != "" {
					t.Error(result.Error)
				}
			}
		}(login)
	}
	close(start)
	wg.Wait()
	if client.http.Jar != original {
		t.Error("authentication replaced the cookie jar used by concurrent requests")
	}
}

func TestCatalogRejectsTrailingOrOversizedData(t *testing.T) {
	raw, _ := json.Marshal(Catalog{Version: 1, Operations: []Operation{{Name: "test", Method: "GET", Path: "/api/v1/test", Transport: "json"}}})
	for _, body := range []string{string(raw) + ` {}`, string(raw) + strings.Repeat(" ", 2<<20)} {
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		client, err := NewClient(ClientOptions{Server: host.URL, Development: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Catalog(context.Background())
		client.Close()
		host.Close()
		if err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
}

func TestClosingTerminalCancelsInFlightResume(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(Catalog{Version: 1, Operations: []Operation{
				{Name: "terminal.resume", Method: "POST", Path: "/api/v1/nodes/{nodeId}/terminals/{streamId}/resume", Parameters: []string{"nodeId", "streamId"}, Transport: "json"},
				{Name: "terminal.cancel", Method: "DELETE", Path: "/api/v1/nodes/{nodeId}/terminals/{streamId}", Parameters: []string{"nodeId", "streamId"}, Transport: "json"},
			}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case "/api/v1/nodes/node/terminals/stream/resume":
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "/api/v1/nodes/node/terminals/stream":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	defer close(release)
	client, err := NewClient(ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal := &TerminalSession{client: client, nodeID: "node", streamID: "stream"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := terminal.Read(ctx); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("resume not started")
	}
	if err := terminal.CloseNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("closed terminal retained an in-flight resume request")
	}
}
