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

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	"github.com/CST-Cat/NodeDance/internal/core/config"
	coreprefs "github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/core/feeds"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	coreretention "github.com/CST-Cat/NodeDance/internal/core/retention"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/core/webassets"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
)

type Options struct {
	JSONReadTimeout           time.Duration
	TransferReadTimeout       time.Duration
	MaxConcurrentRequests     int
	UpdateInvoke              func(context.Context, update.RPC) (update.Response, error)
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
}

type Server struct {
	feeds                      *feeds.Store
	guestMu                    sync.Mutex
	guestSlots                 chan struct{}
	guestCache                 *guestDisplay
	guestScope                 [32]byte
	requestSlots               chan struct{}
	jsonReadTimeout            time.Duration
	transferReadTimeout        time.Duration
	httpChannelsMu             sync.Mutex
	httpChannels               map[string]*httpAgentChannel
	updateInvoke               func(context.Context, update.RPC) (update.Response, error)
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
	composeProjects            *corecompose.Store
	composeContentMu           sync.Mutex
	composeContents            map[string]pendingComposeContent
	fileContentMu              sync.Mutex
	fileContents               map[string]pendingFileContent
	containerCreateMu          sync.Mutex
	containerCreateSpecs       map[string]pendingContainerCreateSpec
	imageAuthMu                sync.Mutex
	imageAuth                  map[string]pendingImageCredential
	tailscaleResolveMu         sync.Mutex
	probes                     *coreprobes.Store
	alerts                     *corealerts.Store
	alertSender                *corealerts.Sender
	dockerMu                   sync.Mutex
	docker                     *coredocker.Store
	dockerRestoreFailed        bool
	dockerRestoreFailedNodes   map[string]string
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
	terminalSessionMu          sync.Mutex
	fileTransferLimit          int64
	nonMetricHistoryRetention  time.Duration
	historyRetentionInterval   time.Duration
	preferenceMigrator         *coreprefs.SQLiteMigrator
}

type session struct {
	Persistent  bool
	UserID      int64
	Role        string
	Username    string
	DisplayName string
	ID          string
	Digest      []byte
	CSRFDigest  []byte
	CreatedAt   time.Time
	LastSeenAt  time.Time
	UserAgent   string
	RemoteAddr  string
}

func New(version string, options Options) (*Server, error) {
	if options.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if options.JSONReadTimeout == 0 {
		options.JSONReadTimeout = 15 * time.Second
	}
	if options.TransferReadTimeout == 0 {
		options.TransferReadTimeout = 30 * time.Minute
	}
	if options.MaxConcurrentRequests == 0 {
		options.MaxConcurrentRequests = 256
	}
	if options.JSONReadTimeout <= 0 || options.TransferReadTimeout <= 0 || options.MaxConcurrentRequests < 1 || options.MaxConcurrentRequests > 4096 {
		return nil, errors.New("invalid request resource limits")
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
	s := &Server{
		guestSlots:   make(chan struct{}, 4),
		version:      version,
		updateInvoke: options.UpdateInvoke,
		assets:       http.FileServer(http.FS(files)),
		index:        index,
		store:        store,
		requestSlots: make(chan struct{}, options.MaxConcurrentRequests), jsonReadTimeout: options.JSONReadTimeout, transferReadTimeout: options.TransferReadTimeout,
		dataDir:                   store.Dir,
		development:               options.Development,
		publicOrigin:              strings.TrimSuffix(options.PublicOrigin, "/"),
		trusted:                   trusted,
		idleTimeout:               options.SessionIdleTimeout,
		limiter:                   newLoginLimiter(options.LoginMaxAttempts, options.LoginLockoutDuration, options.Now),
		passwordSlots:             make(chan struct{}, 2),
		websocketCheckInterval:    options.WebSocketCheckInterval,
		now:                       options.Now,
		hashSetupPassword:         auth.HashPassword,
		agents:                    agents.NewRepository(store.DB, options.Now),
		metrics:                   coremetrics.NewStore(),
		history:                   &corehistory.Store{DB: store.DB},
		dashboardPreferences:      &dashboard.Repository{DB: store.DB, Now: options.Now},
		docker:                    coredocker.NewStore(),
		imageAuth:                 make(map[string]pendingImageCredential),
		composeContents:           make(map[string]pendingComposeContent),
		fileContents:              make(map[string]pendingFileContent),
		containerCreateSpecs:      make(map[string]pendingContainerCreateSpec),
		agentOfflineTimeout:       options.AgentOfflineTimeout,
		agentSweepInterval:        options.AgentSweepInterval,
		agentConnections:          make(map[string]*agentConnection),
		agentLeaseWatchers:        make(map[string]*agentConnection),
		containerStreamSlots:      make(chan struct{}, 64),
		terminals:                 newTerminalStreamManager(),
		fileTransferLimit:         options.FileTransferLimit,
		nonMetricHistoryRetention: options.NonMetricHistoryRetention,
		historyRetentionInterval:  options.HistoryRetentionInterval,
		preferenceMigrator:        coreprefs.NewSQLiteMigrator(store.DB),
	}
	s.composeProjects, err = corecompose.NewStore(store.DB, corecompose.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize Compose project registry: %w", err)
	}
	s.tasks, err = coretasks.New(store.DB, coretasks.Options{Now: options.Now})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize durable Core task store: %w", err)
	}
	if err := s.tasks.RecoverLocalTasks(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover interrupted Core-local tasks: %w", err)
	}
	s.probes = coreprobes.New(store.DB, options.Now)
	alertKey, err := loadOrCreateAlertEncryptionKey(store.Dir)
	if err != nil {
		log.Printf("NodeDance alerts unavailable; Core management remains active: %v", err)
	} else {
		alerts, alertErr := corealerts.NewStore(store.DB, alertKey, options.Now)
		if alertErr == nil {
			alertErr = alerts.RecoverDeliveries(context.Background())
		}
		if alertErr != nil {
			log.Printf("NodeDance alerts unavailable; Core management remains active: %v", alertErr)
		} else {
			s.alerts = alerts
			s.alertSender = corealerts.NewSender()
		}
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
		log.Printf("NodeDance service probe startup state could not be updated; Core management remains active: %v", err)
	}
	if err := s.probes.RecoverPending(context.Background(), options.Now()); err != nil {
		log.Printf("NodeDance pending service probes could not be recovered; Core management remains active: %v", err)
	}
	savedDocker, err := loadDockerNodes(context.Background(), store.DB)
	if err != nil {
		s.dockerRestoreFailed = true
		log.Printf("NodeDance persisted Docker state could not be restored; Core management remains active: %v", err)
	} else {
		s.dockerRestoreFailedNodes = make(map[string]string)
		for _, saved := range savedDocker {
			if err := s.docker.RestoreStale(saved); err != nil {
				s.dockerRestoreFailedNodes[saved.Identity.NodeID] = "docker_node_state_restore_failed"
				log.Printf("NodeDance persisted Docker state for node %s could not be restored: %v", saved.Identity.NodeID, err)
			}
		}
	}
	s.feeds = feeds.New(store.DB, s.now)
	if err := s.feeds.Recover(context.Background()); err != nil {
		log.Printf("NodeDance subscription recovery failed: %v", err)
		s.feeds = nil
	}
	s.agentContext, s.agentCancel = context.WithCancel(context.Background())
	if s.feeds != nil {
		s.agentWait.Add(1)
		go func() {
			defer s.agentWait.Done()
			s.feeds.Run(s.agentContext, func(ctx context.Context, id, payload string) error {
				if s.alerts == nil || s.alertSender == nil {
					return errors.New("notifications are unavailable")
				}
				c, secret, e := s.alerts.NotificationChannel(ctx, id)
				if e != nil {
					return e
				}
				_, e = s.alertSender.Send(ctx, c, secret, payload)
				return e
			})
		}()
	}
	s.agentWait.Add(1)
	go s.agentOfflineSweeper()
	s.agentWait.Add(1)
	go s.historyRetentionWorker()
	s.agentWait.Add(1)
	go s.serviceProbeScheduler()
	if s.alerts != nil {
		s.agentWait.Add(1)
		go s.alertEvaluationScheduler()
		s.agentWait.Add(1)
		go s.alertDeliveryScheduler()
	}
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
	s.clearAllImageCredentials()
	s.clearAllComposeContent()
	s.clearAllFileContent()
	s.clearAllContainerCreateSpecs()
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
	operation, known := control.MatchRoute(r.Method, r.URL.Path)
	// Bound rejected bodies too: net/http may drain unread request content.
	if r.Body != nil && r.ContentLength != 0 {
		duration := s.jsonReadTimeout
		if known && (operation.Transport == "upload" || operation.Transport == "multipart") {
			duration = s.transferReadTimeout
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(duration))
		// net/http resets the deadline at the next request. Keeping it prevents
		// a slow rejected or timed-out body being drained indefinitely.
	}
	if r.URL.Path != "/api/v1/health" {
		select {
		case s.requestSlots <- struct{}{}:
			defer func() { <-s.requestSlots }()
		default:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Connection", "close")
			http.Error(w, "Core request capacity reached", http.StatusServiceUnavailable)
			return
		}
	}
	if known {
		for name, description := range operation.Headers {
			if value := r.Header.Get(name); value != "" {
				raw, _ := json.Marshal(value)
				if err := control.ValidateJSON(description, raw); err != nil {
					w.Header().Set("Connection", "close")
					http.Error(w, name+": "+err.Error(), http.StatusBadRequest)
					return
				}
			}
		}
	}

	switch r.URL.Path {
	case "/api/v1/operations":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		catalog := control.Describe()
		for i := range catalog.Operations {
			if catalog.Operations[i].Name == "files.upload" {
				catalog.Operations[i].Upload.MaxBytes = s.fileTransferLimit
			}
		}
		writeJSON(w, http.StatusOK, catalog)
		return
	case "/api/v1/protocol":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, protocol.Describe())
		return
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
		if !s.validOrigin(r) {
			http.Error(w, "sign-in origin is not allowed", http.StatusForbidden)
			return
		}
		current, _ := s.authenticateRequest(w, r, false)
		if !s.validCSRFCookie(r, current) {
			http.Error(w, "sign-in security token expired or changed", http.StatusForbidden)
			return
		}
		s.signIn(w, r, current)
		return
	case "/api/v1/public/guest", "/api/v1/public/guest/display":
		s.handleGuestPublic(w, r)
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
		s.handleAgentEnroll(w, r)
		return
	case "/api/v1/agents/identity":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleAgentIdentity(w, r)
		return
	case "/api/v1/agents/channel":
		s.handleHTTPAgentChannel(w, r)
		return
	case "/ws/v1/agent":
		s.handleAgentWebSocket(w, r)
		return
	case "/ws/v1/streams/logs", "/ws/v1/streams/stats":
		s.handleContainerStreamWebSocket(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/agents/channel/") {
		s.handleHTTPAgentChannel(w, r)
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
		if current.Role != "administrator" && (!known || operation.RequiredRole == "administrator") {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), accountContextKey{}, current.UserID))
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
		if err := s.dispatchDueServiceProbes(s.agentContext); err != nil {
			if s.agentContext.Err() != nil {
				return
			}
			log.Printf("NodeDance service probe scheduler failed; retrying: %v", err)
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
		if !connection.capabilityEnabled(protocol.CapabilityProbes) {
			if err := unknown("capability_unavailable"); err != nil && !errors.Is(err, coreprobes.ErrRunState) {
				return err
			}
			continue
		}
		dispatch := protocol.ProbeDispatch{ProbeID: config.ID, RunID: run.RunID, NodeID: config.NodeID,
			IPFamily: config.IPFamily, Kind: config.Kind, Target: config.Target, ExpectedHTTPStatus: config.ExpectedHTTPStatus,
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
	if r.URL.Path == "/api/v1/feeds" || strings.HasPrefix(r.URL.Path, "/api/v1/feeds/") {
		return true
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
