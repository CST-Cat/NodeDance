package control

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

type Client struct {
	base        *url.URL
	origin      string
	http        *http.Client
	sessionPath string
}
type ClientOptions struct {
	Server, Origin, StateDir, CAFile string
	Development                      bool
}
type savedSession struct {
	Server  string         `json:"server"`
	Cookies []*http.Cookie `json:"cookies"`
}
type Input struct {
	Params         map[string]string `json:"params,omitempty"`
	Query          map[string]string `json:"query,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	FileVersion    string            `json:"fileVersion,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
}
type Result struct {
	Operation string          `json:"operation"`
	Status    int             `json:"status"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
	File      string          `json:"file,omitempty"`
}

func (r Result) Err() error {
	if r.Error != "" {
		return fmt.Errorf("%s: %s", r.Operation, r.Error)
	}
	return nil
}

func NewClient(options ClientOptions) (*Client, error) {
	base, err := url.Parse(options.Server)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("--server must be an HTTPS origin without credentials, path, query or fragment")
	}
	if base.Scheme != "https" {
		ip := net.ParseIP(base.Hostname())
		if base.Scheme != "http" || !options.Development || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTP requires --dev and a literal loopback server")
		}
	}
	base.Path = ""
	origin := options.Origin
	if origin == "" {
		origin = base.String()
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Host == "" || parsedOrigin.User != nil || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" || parsedOrigin.Path != "" || parsedOrigin.Scheme != base.Scheme {
		return nil, errors.New("--origin must be an origin with the same scheme as --server")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if options.CAFile != "" {
		pem, err := os.ReadFile(options.CAFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file has no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	jar, _ := cookiejar.New(nil)
	c := &Client{base: base, origin: origin, http: &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if options.StateDir != "" {
		if err := privateDir(options.StateDir); err != nil {
			return nil, err
		}
		c.sessionPath = filepath.Join(options.StateDir, "session.json")
		raw, err := readPrivateFile(c.sessionPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			var saved savedSession
			if err = json.Unmarshal(raw, &saved); err != nil {
				return nil, errors.New("invalid saved CLI session")
			}
			if saved.Server != base.String() {
				return nil, errors.New("saved session belongs to a different server; select another --state-dir")
			}
			jar.SetCookies(base, saved.Cookies)
		}
	}
	return c, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	// FileInfo.Sys is syscall.Stat_t on Unix; descriptor stat avoids type aliases.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("private state directory: %w", err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if !info.IsDir() || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0700 {
		return errors.New("CLI state directory must be owner-owned and mode 0700")
	}
	return nil
}
func readPrivateFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Size > 64*1024 {
		return nil, errors.New("CLI session must be an owner-owned 0600 regular file")
	}
	return io.ReadAll(f)
}
func (c *Client) save() error {
	if c.sessionPath == "" {
		return nil
	}
	if err := privateDir(filepath.Dir(c.sessionPath)); err != nil {
		return err
	}
	raw, err := json.Marshal(savedSession{Server: c.base.String(), Cookies: c.http.Jar.Cookies(c.base)})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.sessionPath), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.sessionPath)
}
func (c *Client) endpoint(o Operation, in Input) (string, error) {
	path, err := o.URLPath(in.Params)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	for k, v := range in.Query {
		found := false
		for _, allowed := range o.Query {
			if k == allowed {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("unknown query parameter %s", k)
		}
		q.Set(k, v)
	}
	target := c.base.String() + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	return target, nil
}
func (c *Client) csrf(ctx context.Context) (string, error) {
	for _, cookie := range c.http.Jar.Cookies(c.base) {
		if cookie.Name == "nodedance_csrf" {
			return cookie.Value, nil
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base.String()+"/api/v1/auth/csrf", nil)
	response, err := c.http.Do(req)
	if err != nil {
		return "", errors.New("could not obtain CSRF token")
	}
	defer response.Body.Close()
	var token struct {
		Token string `json:"token"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&token) != nil || token.Token == "" {
		return "", errors.New("could not obtain CSRF token")
	}
	return token.Token, nil
}
func (c *Client) request(ctx context.Context, o Operation, in Input, body io.Reader) (*http.Request, error) {
	target, err := c.endpoint(o, in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, o.Method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", c.origin)
	req.Header.Set("User-Agent", "NodeDance CLI/TUI")
	if o.Method != "GET" && o.Method != "HEAD" {
		token, err := c.csrf(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-CSRF-Token", token)
	}
	if o.Idempotent {
		key := in.IdempotencyKey
		for name, value := range in.Headers {
			if strings.EqualFold(name, "Idempotency-Key") {
				if key != "" && key != value {
					return nil, errors.New("conflicting idempotency keys")
				}
				key = value
			}
		}
		if key == "" {
			return nil, errors.New("--idempotency-key is required; reuse the same key when retrying the same intent")
		}
		raw, _ := json.Marshal(key)
		if err := ValidateJSON(o.Headers["Idempotency-Key"], raw); err != nil {
			return nil, err
		}
		req.Header.Set("Idempotency-Key", key)
	}
	for name, value := range in.Headers {
		if _, ok := o.Headers[http.CanonicalHeaderKey(name)]; !ok {
			return nil, fmt.Errorf("unknown operation header %s", name)
		}
		raw, _ := json.Marshal(value)
		if err := ValidateJSON(o.Headers[http.CanonicalHeaderKey(name)], raw); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		req.Header.Set(name, value)
	}
	if in.FileVersion != "" {
		if _, ok := o.Headers["X-File-Version"]; !ok {
			return nil, errors.New("operation does not accept a file version")
		}
		req.Header.Set("X-File-Version", in.FileVersion)
	}
	return req, nil
}

// Execute is the common dispatch for every finite remote operation.
// Binary downloads use a caller-owned writer; uploads do not pass through JSON.
func (c *Client) Execute(ctx context.Context, o Operation, in Input, upload io.Reader, download io.Writer) Result {
	result := Result{Operation: o.Name}
	fail := func(err error) Result { result.Error = err.Error(); return result }
	if o.Transport == "websocket" || o.Transport == "local" || o.Transport == "terminal" {
		return fail(errors.New("operation requires its streaming or local executor"))
	}
	var body io.Reader = bytes.NewReader(in.Body)
	contentType := "application/json"
	if len(in.Body) > 0 && !json.Valid(in.Body) {
		return fail(errors.New("body must contain one JSON value"))
	}
	if o.MaxBodyBytes > 0 && int64(len(in.Body)) > o.MaxBodyBytes {
		return fail(errors.New("body exceeds Core request limit"))
	}
	if err := ValidateJSON(o.BodySchema, in.Body); err != nil {
		return fail(err)
	}
	if o.Transport == "download" && download == nil {
		return fail(errors.New("download requires --output"))
	}
	if o.Transport == "upload" || o.Transport == "multipart" {
		if upload == nil {
			return fail(errors.New("upload requires --input"))
		}
		if len(in.Body) > 0 {
			return fail(errors.New("upload does not accept a JSON body"))
		}
		body = upload
		contentType = "application/octet-stream"
		if o.Transport == "multipart" {
			if o.Upload == nil || o.Upload.Field == "" || o.Upload.MaxBytes < 1 {
				return fail(errors.New("Core omitted multipart upload contract"))
			}
			data, err := io.ReadAll(io.LimitReader(upload, o.Upload.MaxBytes+1))
			if err != nil {
				return fail(err)
			}
			if int64(len(data)) > o.Upload.MaxBytes {
				return fail(errors.New("upload exceeds Core size limit"))
			}
			var buf bytes.Buffer
			writer := multipart.NewWriter(&buf)
			part, err := writer.CreateFormFile(o.Upload.Field, "upload")
			if err != nil {
				return fail(err)
			}
			if _, err = part.Write(data); err != nil {
				return fail(err)
			}
			if err = writer.Close(); err != nil {
				return fail(err)
			}
			body = &buf
			contentType = writer.FormDataContentType()
		}
	}
	if o.Name == "auth.login" || o.Name == "auth.setup" {
		jar, _ := cookiejar.New(nil)
		c.http.Jar = jar
	}
	req, err := c.request(ctx, o, in, body)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", contentType)
	if o.Transport == "upload" {
		if f, ok := upload.(*os.File); ok {
			info, err := f.Stat()
			if err != nil {
				return fail(err)
			}
			if info.Mode().IsRegular() && o.Upload != nil && o.Upload.MaxBytes > 0 && info.Size() > o.Upload.MaxBytes {
				return fail(errors.New("upload exceeds Core size limit"))
			}
			if info.Mode().IsRegular() {
				req.ContentLength = info.Size()
			}
		}
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fail(errors.New("Core request failed (check endpoint, TLS and connectivity)"))
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	if err = c.save(); err != nil {
		return fail(fmt.Errorf("save CLI session: %w", err))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fail(fmt.Errorf("Core returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw))))
	}
	if o.Transport == "download" {
		if _, err = io.Copy(download, response.Body); err != nil {
			return fail(err)
		}
		return result
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err != nil {
		return fail(err)
	}
	if len(raw) > 16*1024*1024 {
		return fail(errors.New("response exceeds 16 MiB"))
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if !json.Valid(raw) {
			return fail(errors.New("Core returned invalid JSON"))
		}
		result.Data = raw
		if err := ValidateJSON(o.ResponseSchema, raw); err != nil {
			return fail(fmt.Errorf("Core response violates operation contract: %w", err))
		}
	}
	return result
}
func (c *Client) Connect(ctx context.Context, o Operation, in Input) (*websocket.Conn, error) {
	if o.Transport != "websocket" {
		return nil, errors.New("operation is not a WebSocket")
	}
	target, err := c.endpoint(o, in)
	if err != nil {
		return nil, err
	}
	target = "ws" + strings.TrimPrefix(target, "http")
	headers := http.Header{"Origin": []string{c.origin}}
	conn, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: headers})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("Core stream rejected: HTTP %d", response.StatusCode)
		}
		return nil, errors.New("Core stream connection failed")
	}
	conn.SetReadLimit(2 * 1024 * 1024)
	return conn, nil
}

// Catalog reads the connected Core's contract, rather than a client-side copy.
func (c *Client) Catalog(ctx context.Context) (Catalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String()+"/api/v1/operations", nil)
	if err != nil {
		return Catalog{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Catalog{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Catalog{}, fmt.Errorf("read Core operation catalog: HTTP %d", response.StatusCode)
	}
	var catalog Catalog
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&catalog); err != nil {
		return Catalog{}, err
	}
	if catalog.Version != 1 || len(catalog.Operations) == 0 {
		return Catalog{}, errors.New("invalid Core operation catalog")
	}
	seen := map[string]bool{}
	for _, operation := range catalog.Operations {
		if operation.Name == "" || seen[operation.Name] || (!strings.HasPrefix(operation.Path, "/api/") && !strings.HasPrefix(operation.Path, "/ws/")) {
			return Catalog{}, errors.New("invalid Core operation definition")
		}
		seen[operation.Name] = true
		switch operation.Transport {
		case "json", "upload", "download", "multipart", "websocket", "terminal":
		default:
			return Catalog{}, errors.New("invalid Core operation transport")
		}
		switch operation.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		default:
			return Catalog{}, errors.New("invalid Core operation method")
		}
	}
	return catalog, nil
}

func FindIn(catalog Catalog, name string) (Operation, error) {
	for _, operation := range catalog.Operations {
		if operation.Name == name {
			return operation, nil
		}
	}
	return Operation{}, fmt.Errorf("unknown operation %q", name)
}
