package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTransportCertificateValidatesPairAndPEM(t *testing.T) {
	if _, err := loadTransportCertificate("cert.pem", ""); err == nil || !strings.Contains(err.Error(), "provided together") {
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
	if _, err := loadTransportCertificate(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "load Core TLS certificate and key") {
		t.Fatalf("invalid TLS pair was accepted: %v", err)
	}
}
