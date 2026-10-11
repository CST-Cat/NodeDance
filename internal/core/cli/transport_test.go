package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTransportCertificateRequiresTLSForNonLoopbackListeners(t *testing.T) {
	if _, err := loadTransportCertificate("0.0.0.0:8180", "", ""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("non-loopback plaintext listener was accepted: %v", err)
	}
	if certificate, err := loadTransportCertificate("127.0.0.1:8180", "", ""); err != nil || certificate != nil {
		t.Fatalf("loopback listener without TLS certificate: certificate=%v err=%v", certificate, err)
	}
}

func TestLoadTransportCertificateValidatesPairAndPEM(t *testing.T) {
	if _, err := loadTransportCertificate("0.0.0.0:8180", "cert.pem", ""); err == nil || !strings.Contains(err.Error(), "provided together") {
		t.Fatalf("incomplete TLS pair was accepted: %v", err)
	}
	directory := t.TempDir()
	certPath := filepath.Join(directory, "cert.pem")
	keyPath := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("not a private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTransportCertificate("0.0.0.0:8180", certPath, keyPath); err == nil || !strings.Contains(err.Error(), "load Core TLS certificate and key") {
		t.Fatalf("invalid TLS pair was accepted: %v", err)
	}
}
