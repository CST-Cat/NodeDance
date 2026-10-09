package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	corecomposeedit "github.com/CST-Cat/NodeDance/internal/core/composeedit"
	"github.com/CST-Cat/NodeDance/internal/core/config"
	coreprefs "github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	corefiletasks "github.com/CST-Cat/NodeDance/internal/core/filetasks"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	coreretention "github.com/CST-Cat/NodeDance/internal/core/retention"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	coreupdates "github.com/CST-Cat/NodeDance/internal/core/updates"
	"github.com/CST-Cat/NodeDance/internal/core/webassets"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type Options struct {
	DataDir                   string
	Development               bool
	PublicOrigin              string
	TrustedProxies            []string
	SessionIdleTimeout        time.Duration
	LoginMaxAttempts          int
	LoginLockoutDuration      time.Duration
	WebSocketCheckInterval    time.Duration
	AgentOfflineTimeout       time.Duration
	AgentSweepInterval        time.Duration
	FileTransferLimit         int64
	NonMetricHistoryRetention time.Duration
	HistoryRetentionInterval  time.Duration
	Now                       func() time.Time
	// PreferenceMigrator overrides the production SQLite-backed S06 display-
	// preference identity migration. NopMigrator is available for explicit
	// disabled/test configurations only.
	PreferenceMigrator         coreprefs.Migrator
	AgentUpdatePublicKeyBase64 string
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
	fileTasks                  *corefiletasks.Store
	composeOps                 *corecompose.Store
	composeEditorOps           *corecomposeedit.Store
	imageAuthMu                sync.Mutex
	imageAuth                  map[string]pendingImageCredential
	probes                     *coreprobes.Store
	alerts                     *corealerts.Store
	updates                    *coreupdates.Store
	agentUpdatePublicKey       ed25519.PublicKey
	agentUpdatePublicKeyBase64 string
	alertSender                *corealerts.Sender
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
	terminals                  *terminalStreamManager
	fileTransferLimit          int64
	nonMetricHistoryRetention  time.Duration
	historyRetentionInterval   time.Duration
	preferenceMigrator         coreprefs.Migrator
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
	if options.FileTransferLimit == 0 {
		options.FileTransferLimit = protocol.DefaultFileLimit
	}
	if options.FileTransferLimit < 1 || options.FileTransferLimit > protocol.MaxFileSize {
		return nil, errors.New("file transfer limit is outside the protocol hard limit")
	}
	if options.NonMetricHistoryRetention == 0 {
		options.NonMetricHistoryRetention = time.Duration(coreretention.DefaultDays) * 24 * time.Hour
	}
	if options.NonMetricHistoryRetention < 24*time.Hour || options.NonMetricHistoryRetention > time.Duration(coreretention.MaxDays)*24*time.Hour {
		return nil, errors.New("non-metric history retention must be between 1 and 3650 days")
	}
	if options.HistoryRetentionInterval == 0 {
		options.HistoryRetentionInterval = 6 * time.Hour
	}
	if options.HistoryRetentionInterval <= 0 || options.HistoryRetentionInterval > 24*time.Hour {
		return nil, errors.New("history retention interval must be positive and no greater than 24 hours")
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
	var updatePublicKey ed25519.PublicKey
	if strings.TrimSpace(options.AgentUpdatePublicKeyBase64) != "" {
		updatePublicKey, err = agentupdate.PublicKeyFromBase64(options.AgentUpdatePublicKeyBase64)
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("configure Agent update trust key: %w", err)
		}
	}
	s := &Server{
		version:                    version,
		assets:                     http.FileServer(http.FS(files)),
		index:                      index,
		store:                      store,
		dataDir:                    store.Dir,
		development:                options.Development,
		publicOrigin:               strings.TrimSuffix(options.PublicOrigin, "/"),
		trusted:                    trusted,
		idleTimeout:                options.SessionIdleTimeout,
		limiter:                    newLoginLimiter(options.LoginMaxAttempts, options.LoginLockoutDuration, options.Now),
		passwordSlots:              make(chan struct{}, 2),
		websocketCheckInterval:     options.WebSocketCheckInterval,
		now:                        options.Now,
		hashSetupPassword:          auth.HashPassword,
		agents:                     agents.NewRepository(store.DB, options.Now),
		metrics:                    coremetrics.NewStore(),
		history:                    &corehistory.Store{DB: store.DB},
		dashboardPreferences:       &dashboard.Repository{DB: store.DB, Now: options.Now},
		docker:                     coredocker.NewStore(),
		imageAuth:                  make(map[string]pendingImageCredential),
		agentOfflineTimeout:        options.AgentOfflineTimeout,
		agentSweepInterval:         options.AgentSweepInterval,
		agentConnections:           make(map[string]*agentConnection),
		agentLeaseWatchers:         make(map[string]*agentConnection),
		containerStreamSlots:       make(chan struct{}, 64),
		terminals:                  newTerminalStreamManager(),
		fileTransferLimit:          options.FileTransferLimit,
		nonMetricHistoryRetention:  options.NonMetricHistoryRetention,
		historyRetentionInterval:   options.HistoryRetentionInterval,
		preferenceMigrator:         options.PreferenceMigrator,
		agentUpdatePublicKey:       updatePublicKey,
		agentUpdatePublicKeyBase64: strings.TrimSpace(options.AgentUpdatePublicKeyBase64),
	}
	if s.preferenceMigrator == nil {
		s.preferenceMigrator = coreprefs.NewSQLiteMigrator(store.DB)
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
	s.composeEditorOps, err = corecomposeedit.NewStore(store.DB, corecomposeedit.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize Compose editor operation store: %w", err)
	}
	if _, err := s.composeEditorOps.RecoverUnfinished(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover unfinished Compose editor operations: %w", err)
	}
	s.tasks, err = coretasks.New(store.DB, coretasks.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize durable Core task store: %w", err)
	}
	s.fileTasks, err = corefiletasks.New(store.DB, options.Now)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize durable file task store: %w", err)
	}
	if err := s.fileTasks.RecoverUnfinished(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover unfinished file write tasks: %w", err)
	}
	s.probes = coreprobes.New(store.DB, options.Now)
	s.updates = coreupdates.New(store.DB, options.Now)
	if err := s.updates.Recover(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover Agent update tasks: %w", err)
	}
	alertKey, err := loadOrCreateAlertEncryptionKey(store.Dir)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	s.alerts, err = corealerts.NewStore(store.DB, alertKey, options.Now)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize alert store: %w", err)
	}
	s.alertSender = corealerts.NewSender()
	if err := s.alerts.RecoverDeliveries(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover alert deliveries: %w", err)
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
	if _, err := coreretention.Cleanup(context.Background(), store.DB, options.Now(), options.NonMetricHistoryRetention); err != nil {
		// This transaction is all-or-nothing. Let the Core remain available and
		// report maintenance failures explicitly instead of claiming a purge.
		log.Printf("NodeDance non-metric history retention cleanup failed: %v", err)
	}
	if err := s.agents.MarkAllOffline(context.Background()); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := s.probes.MarkAllNodesUnknown(context.Background(), options.Now()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("mark service probes unknown during startup: %w", err)
	}
	if err := s.probes.RecoverPending(context.Background(), options.Now()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover pending service probes: %w", err)
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
	go s.historyRetentionWorker()
	s.agentWait.Add(1)
	go s.serviceProbeScheduler()
	s.agentWait.Add(1)
	go s.alertEvaluationScheduler()
	s.agentWait.Add(1)
	go s.alertDeliveryScheduler()
	s.agentWait.Add(1)
	go s.agentUpdateScheduler()
	return s, nil
}

func (s *Server) historyRetentionWorker() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(s.historyRetentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
			if err := s.history.Cleanup(s.agentContext, s.now()); err != nil {
				log.Printf("NodeDance metric-history retention cleanup failed: %v", err)
			}
			if _, err := coreretention.Cleanup(s.agentContext, s.store.DB, s.now(), s.nonMetricHistoryRetention); err != nil {
				log.Printf("NodeDance non-metric history retention cleanup failed: %v", err)
			}
		}
	}
}

func (s *Server) Close() error {
	if s.terminals != nil {
		s.terminals.closeAll(s, "Core shutdown")
	}
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

func (s *Server) agentUpdateOrigin(r *http.Request) string {
	if s.publicOrigin != "" {
		return s.publicOrigin
	}
	if r == nil || r.Host == "" || strings.ContainsAny(r.Host, "/\\\r\n\t@") {
		return ""
	}
	scheme := ""
	if r.TLS != nil {
		scheme = "https"
	}
	if scheme == "" && s.isTrustedProxy(remoteIP(r)) {
		forwarded := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		if forwarded == "https" {
			scheme = "https"
		}
	}
	if scheme == "" && s.development && remoteIP(r) != nil && remoteIP(r).IsLoopback() {
		scheme = "http"
	}
	if scheme == "" {
		return ""
	}
	parsed, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || parsed.Host != r.Host || parsed.User != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
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
	case "/api/v1/agent-updates/":
		s.handleAgentUpdateArtifact(w, r)
		return
	case "/ws/v1/agent":
		s.handleAgentWebSocket(w, r)
		return
	case "/ws/v1/streams/logs", "/ws/v1/streams/stats":
		s.handleContainerStreamWebSocket(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/agent-updates/") {
		s.handleAgentUpdateArtifact(w, r)
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

func (s *Server) serviceProbeScheduler() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.dispatchDueServiceProbes(s.agentContext); err != nil && s.agentContext.Err() != nil {
			return
		}
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) dispatchDueServiceProbes(ctx context.Context) error {
	now := s.now().UTC()
	if err := s.probes.ExpirePending(ctx, now); err != nil {
		return err
	}
	due, err := s.probes.Due(ctx, now, 64)
	if err != nil {
		return err
	}
	for _, candidate := range due {
		connection := s.agentConnectionForNode(candidate.NodeID)
		if connection == nil {
			// Do not claim an executable run for an offline Agent. Persist the
			// state transition once and defer the next check by the probe interval.
			if err := s.probes.MarkNodeUnknown(ctx, candidate.NodeID, now); err != nil {
				return err
			}
			continue
		}
		generation := connection.generation
		config, run, claimed, err := s.probes.Claim(ctx, candidate.ID, generation, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		unknown := func(code string) error {
			return s.probes.Complete(ctx, protocol.ProbeReport{ProbeID: run.ProbeID, RunID: run.RunID,
				NodeID: run.NodeID, Status: protocol.ProbeResultUnknown, ErrorCode: code}, generation, s.now())
		}
		if !connection.probeEnabled {
			if err := unknown("capability_unavailable"); err != nil && !errors.Is(err, coreprobes.ErrRunState) {
				return err
			}
			continue
		}
		dispatch := protocol.ProbeDispatch{ProbeID: config.ID, RunID: run.RunID, NodeID: config.NodeID,
			Kind: config.Kind, Target: config.Target, ExpectedHTTPStatus: config.ExpectedHTTPStatus,
			IntervalSeconds: config.IntervalSeconds, TimeoutSeconds: config.TimeoutSeconds}
		payload, err := json.Marshal(dispatch)
		if err != nil {
			return err
		}
		command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProbeDispatch,
			Generation: generation, RequestID: run.RunID, Payload: payload}
		select {
		case connection.commands <- command:
		default:
			if err := unknown("dispatch_unavailable"); err != nil && !errors.Is(err, coreprobes.ErrRunState) {
				return err
			}
		}
	}
	return nil
}

func (s *Server) agentConnectionForNode(nodeID string) *agentConnection {
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID {
			return connection
		}
	}
	return nil
}

func isMetricsTelemetryRead(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	if r.URL.Path == "/api/v1/alerts" || strings.HasPrefix(r.URL.Path, "/api/v1/alerts/") {
		return true
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
