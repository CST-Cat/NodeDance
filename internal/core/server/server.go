package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/config"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/core/webassets"
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
	return s, nil
}

func (s *Server) Close() error {
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
	}

	if strings.HasPrefix(r.URL.Path, "/ws/") {
		s.handleWebSocket(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		current, ok := s.authenticateRequest(w, r, true)
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
