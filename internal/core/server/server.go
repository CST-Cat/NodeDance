package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	"github.com/CST-Cat/NodeDance/internal/core/config"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/core/webassets"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type Options struct {
	DataDir                string
	Development            bool
	PublicOrigin           string
	TrustedProxies         []string
	SessionIdleTimeout     time.Duration
	LoginMaxAttempts       int
	LoginLockoutDuration   time.Duration
	WebSocketCheckInterval time.Duration
	AgentOfflineTimeout    time.Duration
	AgentSweepInterval     time.Duration
	Now                    func() time.Time
}

type Server struct {
	version                    string
	assets                     http.Handler
	index                      []byte
	store                      *storage.Store
	dataDir                    string
	development                bool
	publicOrigin               string
	trusted                    []*net.IPNet
	idleTimeout                time.Duration
	limiter                    *loginLimiter
	passwordSlots              chan struct{}
	websocketCheckInterval     time.Duration
	now                        func() time.Time
	csrfKey                    []byte
	loginVerifiedHook          func()
	passwordChangeVerifiedHook func()
	hashSetupPassword          func(string) ([]byte, []byte, error)
	agents                     *agents.Repository
	metrics                    *coremetrics.Store
	history                    *corehistory.Store
	dashboardPreferences       *dashboard.Repository
	tasks                      *coretasks.Store
	composeOps                 *corecompose.Store
	dockerMu                   sync.Mutex
	docker                     *coredocker.Store
	agentOfflineTimeout        time.Duration
	agentSweepInterval         time.Duration
	agentConnectionsMu         sync.Mutex
	agentConnections           map[string]*agentConnection
	agentLifecycleMu           sync.Mutex
	agentClosing               bool
	agentContext               context.Context
	agentCancel                context.CancelFunc
	agentWait                  sync.WaitGroup
	agentLeaseWatchers         map[string]*agentConnection
	containerStreamSlots       chan struct{}
}

type session struct {
	ID         string
	Digest     []byte
	CSRFDigest []byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	RemoteAddr string
}

func New(version string, options Options) (*Server, error) {
	if options.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if options.SessionIdleTimeout == 0 {
		options.SessionIdleTimeout = config.DefaultSessionIdleTimeout
	}
	if options.SessionIdleTimeout <= 0 || options.SessionIdleTimeout > 24*time.Hour {
		return nil, errors.New("session idle timeout must be greater than zero and no more than 24 hours")
	}
	if options.LoginMaxAttempts == 0 {
		options.LoginMaxAttempts = config.DefaultLoginMaxAttempts
	}
	if options.LoginMaxAttempts < 1 || options.LoginMaxAttempts > 100 {
		return nil, errors.New("login maximum attempts must be between 1 and 100")
	}
	if options.LoginLockoutDuration == 0 {
		options.LoginLockoutDuration = config.DefaultLoginLockoutDuration
	}
	if options.LoginLockoutDuration <= 0 || options.LoginLockoutDuration > 24*time.Hour {
		return nil, errors.New("login lockout duration must be greater than zero and no more than 24 hours")
	}
	if options.WebSocketCheckInterval == 0 {
		options.WebSocketCheckInterval = 10 * time.Second
	}
	if options.WebSocketCheckInterval <= 0 || options.WebSocketCheckInterval > 30*time.Second {
		return nil, errors.New("WebSocket session check interval must be between 0 and 30 seconds")
	}
	if options.AgentOfflineTimeout == 0 {
		options.AgentOfflineTimeout = time.Duration(protocol.OfflineAfterSeconds) * time.Second
	}
	if options.AgentOfflineTimeout <= 0 || options.AgentOfflineTimeout > 30*time.Second {
		return nil, errors.New("Agent offline timeout must be greater than zero and no more than 30 seconds")
	}
	if !options.Development && options.AgentOfflineTimeout != time.Duration(protocol.OfflineAfterSeconds)*time.Second {
		return nil, errors.New("Agent offline timeout override is accepted only in --dev mode")
	}
	if options.AgentSweepInterval == 0 {
		options.AgentSweepInterval = time.Second
	}
	if options.AgentSweepInterval <= 0 || options.AgentSweepInterval > options.AgentOfflineTimeout {
		return nil, errors.New("Agent offline sweep interval must be positive and no greater than its timeout")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := config.ValidatePublicOrigin(options.PublicOrigin, options.Development); err != nil {
		return nil, err
	}
	trusted, err := config.ParseTrustedProxies(options.TrustedProxies)
	if err != nil {
		return nil, err
	}
	files, err := fs.Sub(webassets.Files, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, err
	}
	store, err := storage.Open(context.Background(), options.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		version:                version,
		assets:                 http.FileServer(http.FS(files)),
		index:                  index,
		store:                  store,
		dataDir:                store.Dir,
		development:            options.Development,
		publicOrigin:           strings.TrimSuffix(options.PublicOrigin, "/"),
		trusted:                trusted,
		idleTimeout:            options.SessionIdleTimeout,
		limiter:                newLoginLimiter(options.LoginMaxAttempts, options.LoginLockoutDuration, options.Now),
		passwordSlots:          make(chan struct{}, 2),
		websocketCheckInterval: options.WebSocketCheckInterval,
		now:                    options.Now,
		hashSetupPassword:      auth.HashPassword,
		agents:                 agents.NewRepository(store.DB, options.Now),
		metrics:                coremetrics.NewStore(),
		history:                &corehistory.Store{DB: store.DB},
		dashboardPreferences:   &dashboard.Repository{DB: store.DB, Now: options.Now},
		docker:                 coredocker.NewStore(),
		agentOfflineTimeout:    options.AgentOfflineTimeout,
		agentSweepInterval:     options.AgentSweepInterval,
		agentConnections:       make(map[string]*agentConnection),
		agentLeaseWatchers:     make(map[string]*agentConnection),
		containerStreamSlots:   make(chan struct{}, 64),
	}
	s.composeOps, err = corecompose.NewStore(store.DB, corecompose.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize Compose operation store: %w", err)
	}
	if _, err := s.composeOps.RecoverUnfinished(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover unfinished Compose operations: %w", err)
	}
	s.tasks, err = coretasks.New(store.DB, coretasks.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize durable Core task store: %w", err)
	}
	s.csrfKey, err = loadOrCreateSigningKey(store.Dir)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := s.ensureSetupState(); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := s.history.Cleanup(context.Background(), options.Now()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("clean expired metric history: %w", err)
	}
	if err := s.agents.MarkAllOffline(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	savedDocker, err := loadDockerNodes(context.Background(), store.DB)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	for _, saved := range savedDocker {
		if err := s.docker.RestoreStale(saved); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	s.agentContext, s.agentCancel = context.WithCancel(context.Background())
	s.agentWait.Add(1)
	go s.agentOfflineSweeper()
	s.agentWait.Add(1)
	go s.metricHistoryRetentionWorker()
	return s, nil
}

func (s *Server) metricHistoryRetentionWorker() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
			if err := s.history.Cleanup(s.agentContext, s.now()); err != nil {
				log.Printf("NodeDance metric-history retention cleanup failed: %v", err)
			}
		}
	}
}

func (s *Server) Close() error {
	s.agentLifecycleMu.Lock()
	s.agentClosing = true
	if s.agentCancel != nil {
		s.agentCancel()
	}
	s.agentLifecycleMu.Unlock()
	s.agentConnectionsMu.Lock()
	for agentID, connection := range s.agentConnections {
		connection.close()
		delete(s.agentConnections, agentID)
	}
	s.agentConnectionsMu.Unlock()
	s.agentWait.Wait()
	if s.agents != nil {
		_ = s.agents.MarkAllOffline(context.Background())
	}
	return s.store.Close()
}

func (s *Server) SetupCredentialPath() (string, bool) {
	initialized, err := s.isInitialized()
	if err != nil || initialized {
		return "", false
	}
	path := setupCredentialPath(s.dataDir)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.setSecurityHeaders(w)
	switch r.URL.Path {
	case "/api/v1/health":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
		return
	case "/api/v1/auth/csrf":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleCSRFIssue(w, r)
		return
	case "/api/v1/auth/setup/status":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSetupStatus(w)
		return
	case "/api/v1/auth/setup":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.validOrigin(r) || !s.validCSRFCookie(r, nil) {
			http.Error(w, "request rejected", http.StatusForbidden)
			return
		}
		s.handleSetup(w, r)
		return
	case "/api/v1/auth/login":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.validOrigin(r) || !s.validCSRFCookie(r, nil) {
			http.Error(w, "request rejected", http.StatusForbidden)
			return
		}
		s.handleLogin(w, r)
		return
	case "/api/v1/public/appearance":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePublicAppearance(w)
		return
	case "/api/v1/public/appearance/avatar", "/api/v1/public/appearance/background":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePublicImage(w, r)
		return
	case "/api/v1/agents/enroll":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.secureAgentRequest(r) {
			http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
			return
		}
		s.handleAgentEnroll(w, r)
		return
	case "/api/v1/agents/identity":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.secureAgentRequest(r) {
			http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
			return
		}
		s.handleAgentIdentity(w, r)
		return
	case "/ws/v1/agent":
		s.handleAgentWebSocket(w, r)
		return
	case "/ws/v1/streams/logs", "/ws/v1/streams/stats":
		s.handleContainerStreamWebSocket(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/ws/") {
		s.handleWebSocket(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		// The node dashboard polls these two read-only telemetry endpoints in
		// the background. They still require a live session, but polling must
		// not keep an otherwise idle browser session alive indefinitely.
		current, ok := s.authenticateRequest(w, r, !isMetricsTelemetryRead(r))
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if isUnsafeMethod(r.Method) && (!s.validOrigin(r) || !s.validCSRFCookie(r, current)) {
			http.Error(w, "request rejected", http.StatusForbidden)
			return
		}
		s.handlePrivateAPI(w, r, current)
		return
	}
	if r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/settings" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/settings" {
			if _, ok := s.authenticateRequest(w, r, false); !ok {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(s.index)
		}
		return
	}
	if strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.assets.ServeHTTP(w, r)
}

func isMetricsTelemetryRead(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	if r.URL.Path == "/api/v1/nodes" {
		return true
	}
	if _, _, ok := dockerRoute(r.URL.Path); ok {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") || !strings.HasSuffix(r.URL.Path, "/metrics") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	return len(parts) == 2 && parts[1] == "metrics" && validUUID(parts[0])
}

func (s *Server) setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ http.Handler = (*Server)(nil)
