package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	coreserver "github.com/CST-Cat/NodeDance/internal/core/server"
)

const (
	adminPassword = "S03-guest-test-password"
	adminName     = "S03 Guest Harness"
)

type manifest struct {
	ServerURL       string `json:"serverUrl"`
	CAFile          string `json:"caFile"`
	CAHostFile      string `json:"caHostFile"`
	ControlURL      string `json:"controlUrl"`
	EnrollmentToken string `json:"enrollmentToken"`
	NodeID          string `json:"nodeId"`
}

type readyMessage struct {
	CoreURL     string `json:"coreUrl"`
	GuestURL    string `json:"guestUrl"`
	ControlURL  string `json:"controlUrl"`
	Manifest    string `json:"manifest"`
	NodeID      string `json:"nodeId"`
	Certificate string `json:"caCertificate"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "S03 guest Core harness:", err)
		os.Exit(1)
	}
}

func run() error {
	work := strings.TrimSpace(os.Getenv("NODEDANCE_S03_GUEST_CORE_WORK"))
	if work == "" {
		return errors.New("NODEDANCE_S03_GUEST_CORE_WORK is required")
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	coreListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return err
	}
	port := coreListener.Addr().(*net.TCPAddr).Port
	guestURL := fmt.Sprintf("https://10.0.2.2:%d", port)
	coreURL := fmt.Sprintf("https://127.0.0.1:%d", port)
	caPEM, certPEM, keyPEM, err := createTestCertificates()
	if err != nil {
		_ = coreListener.Close()
		return err
	}
	caPath := filepath.Join(work, "core-ca.pem")
	certPath := filepath.Join(work, "core-server.pem")
	keyPath := filepath.Join(work, "core-server-key.pem")
	for path, data := range map[string][]byte{caPath: caPEM, certPath: certPEM, keyPath: keyPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			_ = coreListener.Close()
			return err
		}
	}

	core, err := coreserver.New("s03-guest-test", coreserver.Options{
		DataDir: filepath.Join(work, "core-data"), Development: true, PublicOrigin: guestURL,
		AgentOfflineTimeout: 8 * time.Second, AgentSweepInterval: 250 * time.Millisecond,
	})
	if err != nil {
		_ = coreListener.Close()
		return err
	}
	coreHTTP := &http.Server{Handler: core, ReadHeaderTimeout: 5 * time.Second}
	coreDone := make(chan error, 1)
	go func() { coreDone <- coreHTTP.ServeTLS(coreListener, certPath, keyPath) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = coreHTTP.Shutdown(ctx)
		_ = core.Close()
	}()

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caPEM) {
		return errors.New("cannot add generated Core test CA")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Jar: jar, Timeout: 8 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: rootPool, MinVersion: tls.VersionTLS12},
	}}
	setupPath, ok := core.SetupCredentialPath()
	if !ok {
		return errors.New("Core setup credential is unavailable")
	}
	setupCredential, err := os.ReadFile(setupPath)
	if err != nil {
		return err
	}
	var csrf struct {
		Token string `json:"token"`
	}
	if err := doJSON(client, http.MethodGet, coreURL+"/api/v1/auth/csrf", nil, guestURL, "", &csrf); err != nil {
		return fmt.Errorf("issue setup CSRF token: %w", err)
	}
	var setup struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := doJSON(client, http.MethodPost, coreURL+"/api/v1/auth/setup", map[string]string{
		"credential": strings.TrimSpace(string(setupCredential)), "password": adminPassword, "displayName": adminName,
	}, guestURL, csrf.Token, &setup); err != nil {
		return fmt.Errorf("initialize temporary Core admin: %w", err)
	}
	if setup.CSRFToken == "" {
		return errors.New("setup did not return an authenticated CSRF token")
	}
	var enrollment struct {
		NodeID string `json:"nodeId"`
		Token  string `json:"token"`
	}
	if err := doJSON(client, http.MethodPost, coreURL+"/api/v1/agents/enrollments",
		map[string]string{"displayName": "s03-clock-reboot-guest"}, guestURL, setup.CSRFToken, &enrollment); err != nil {
		return fmt.Errorf("create guest Agent enrollment: %w", err)
	}
	if enrollment.NodeID == "" || enrollment.Token == "" {
		return errors.New("Core returned an empty guest Agent enrollment")
	}
	manifestPath := filepath.Join(work, "agent-manifest.json")
	manifestData, err := json.MarshalIndent(manifest{
		ServerURL: guestURL, CAFile: "/etc/nodedance/core-ca.pem", CAHostFile: caPath,
		EnrollmentToken: enrollment.Token, NodeID: enrollment.NodeID,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return err
	}

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
			coreURL+"/api/v1/nodes/"+enrollment.NodeID+"/metrics", nil)
		if err != nil {
			http.Error(w, "cannot create metrics request", http.StatusInternalServerError)
			return
		}
		request.Header.Set("Origin", guestURL)
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "Core metrics query failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	controlHTTP := &http.Server{Handler: controlMux, ReadHeaderTimeout: 2 * time.Second}
	controlDone := make(chan error, 1)
	go func() { controlDone <- controlHTTP.Serve(controlListener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = controlHTTP.Shutdown(ctx)
	}()
	controlURL := "http://" + controlListener.Addr().String() + "/state"
	manifestData, err = json.MarshalIndent(manifest{
		ServerURL: guestURL, CAFile: "/etc/nodedance/core-ca.pem", CAHostFile: caPath,
		ControlURL: controlURL, EnrollmentToken: enrollment.Token, NodeID: enrollment.NodeID,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return err
	}
	ready, _ := json.Marshal(readyMessage{
		CoreURL: coreURL, GuestURL: guestURL, ControlURL: controlURL,
		Manifest: manifestPath, NodeID: enrollment.NodeID, Certificate: caPath,
	})
	fmt.Printf("READY %s\n", ready)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-coreDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("Core server stopped: %w", err)
		}
	case err := <-controlDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("control server stopped: %w", err)
		}
	}
	return nil
}

func createTestCertificates() ([]byte, []byte, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NodeDance S03 guest test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "NodeDance S03 guest Core"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.2.2")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return caPEM, certPEM, keyPEM, nil
}

func doJSON(client *http.Client, method, target string, body any, origin, csrf string, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
	}
	return nil
}
