package alerts

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

type smtpFixtureResult struct {
	message       string
	authBeforeTLS bool
	authAfterTLS  bool
	tlsActive     bool
	err           error
}

func startSMTPFixture(t *testing.T, mode string, certificate tls.Certificate) (int, <-chan smtpFixtureResult) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan smtpFixtureResult, 1)
	go func() {
		var capture smtpFixtureResult
		raw, acceptErr := listener.Accept()
		if acceptErr != nil {
			capture.err = acceptErr
			result <- capture
			return
		}
		defer raw.Close()
		conn := net.Conn(raw)
		capture.tlsActive = mode == SMTPSecurityImplicitTLS
		if mode == SMTPSecurityImplicitTLS {
			tlsConn := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
			if err := tlsConn.Handshake(); err != nil {
				capture.err = err
				result <- capture
				return
			}
			conn = tlsConn
		}
		if _, err := io.WriteString(conn, "220 local fixture ESMTP\r\n"); err != nil {
			capture.err = err
			result <- capture
			return
		}
		reader := bufio.NewReader(conn)
		inData := false
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				capture.err = readErr
				result <- capture
				return
			}
			upper := strings.ToUpper(line)
			if inData {
				if line == ".\r\n" {
					inData = false
					if _, err := io.WriteString(conn, "250 queued\r\n"); err != nil {
						capture.err = err
						result <- capture
						return
					}
					continue
				}
				capture.message += line
				continue
			}
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				_, _ = io.WriteString(conn, "250-local fixture\r\n")
				if mode == SMTPSecuritySTARTTLS && !capture.tlsActive {
					_, _ = io.WriteString(conn, "250-STARTTLS\r\n")
				}
				if capture.tlsActive {
					_, _ = io.WriteString(conn, "250-AUTH PLAIN\r\n")
				}
				_, _ = io.WriteString(conn, "250 SIZE 1048576\r\n")
			case strings.HasPrefix(upper, "STARTTLS"):
				if mode != SMTPSecuritySTARTTLS || capture.tlsActive {
					_, _ = io.WriteString(conn, "454 unavailable\r\n")
					continue
				}
				if _, err := io.WriteString(conn, "220 ready for TLS\r\n"); err != nil {
					capture.err = err
					result <- capture
					return
				}
				tlsConn := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
				if err := tlsConn.Handshake(); err != nil {
					capture.err = err
					result <- capture
					return
				}
				conn = tlsConn
				reader = bufio.NewReader(conn)
				capture.tlsActive = true
			case strings.HasPrefix(upper, "AUTH "):
				if capture.tlsActive {
					capture.authAfterTLS = true
				} else {
					capture.authBeforeTLS = true
				}
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case strings.HasPrefix(upper, "DATA"):
				inData = true
				_, _ = io.WriteString(conn, "354 end with dot\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				_, _ = io.WriteString(conn, "221 bye\r\n")
				result <- capture
				return
			default:
				_, _ = io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	return port, result
}

func smtpFixtureCertificate(t *testing.T, dnsNames []string, ipAddresses []net.IP) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NodeDance SMTP test root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "NodeDance SMTP fixture"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: dnsNames,
		IPAddresses: ipAddresses, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return certificate, roots
}

func smtpTestConfig(port int, securityMode string) ChannelConfig {
	return ChannelConfig{
		SMTPHost: "127.0.0.1", SMTPPort: port, SMTPFrom: "NodeDance <admin@example.test>",
		SMTPTo: "ops@example.test", SMTPUsername: "operator", SMTPSecurityMode: securityMode,
	}
}

func sendSMTPFixture(t *testing.T, sender *Sender, config ChannelConfig) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return sender.sendSMTP(ctx, config, "fixture-password", `{"event":"firing","ruleName":"SMTP test","message":"NodeDance fixture"}`)
}

func waitSMTPFixture(t *testing.T, result <-chan smtpFixtureResult) smtpFixtureResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(4 * time.Second):
		t.Fatal("SMTP fixture did not finish")
		return smtpFixtureResult{}
	}
}

func TestSMTPStartTLSCompatibilityAndNoDowngrade(t *testing.T) {
	cert, roots := smtpFixtureCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	for _, mode := range []string{SMTPSecurityLegacy, SMTPSecuritySTARTTLS} {
		t.Run(fmt.Sprintf("mode_%q", mode), func(t *testing.T) {
			port, result := startSMTPFixture(t, SMTPSecuritySTARTTLS, cert)
			sender := NewSender()
			sender.smtpRootCAs = roots
			if err := sendSMTPFixture(t, sender, smtpTestConfig(port, mode)); err != nil {
				t.Fatalf("send with STARTTLS mode %q: %v", mode, err)
			}
			capture := waitSMTPFixture(t, result)
			if capture.err != nil || !capture.tlsActive || !capture.authAfterTLS || capture.authBeforeTLS || !strings.Contains(capture.message, "NodeDance fixture") {
				t.Fatalf("STARTTLS fixture did not receive a protected message: %#v", capture)
			}
		})
	}

	port, result := startSMTPFixture(t, "plain", tls.Certificate{})
	if err := sendSMTPFixture(t, NewSender(), smtpTestConfig(port, SMTPSecuritySTARTTLS)); err == nil {
		t.Fatal("explicit STARTTLS unexpectedly downgraded to plaintext")
	}
	if capture := waitSMTPFixture(t, result); capture.authBeforeTLS || capture.authAfterTLS {
		t.Fatalf("SMTP credentials were sent despite missing STARTTLS: %#v", capture)
	}
}

func TestSMTPImplicitTLSUsesVerifiedLocalTrustRoot(t *testing.T) {
	cert, roots := smtpFixtureCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	port, result := startSMTPFixture(t, SMTPSecurityImplicitTLS, cert)
	sender := NewSender()
	sender.smtpRootCAs = roots
	if err := sendSMTPFixture(t, sender, smtpTestConfig(port, SMTPSecurityImplicitTLS)); err != nil {
		t.Fatalf("implicit TLS send: %v", err)
	}
	capture := waitSMTPFixture(t, result)
	if capture.err != nil || !capture.tlsActive || !capture.authAfterTLS || capture.authBeforeTLS || !strings.Contains(capture.message, "NodeDance fixture") {
		t.Fatalf("implicit TLS fixture did not receive a protected message: %#v", capture)
	}
}

func TestSMTPImplicitTLSRejectsUntrustedAndMismatchedCertificates(t *testing.T) {
	t.Run("untrusted", func(t *testing.T) {
		cert, _ := smtpFixtureCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
		port, result := startSMTPFixture(t, SMTPSecurityImplicitTLS, cert)
		if err := sendSMTPFixture(t, NewSender(), smtpTestConfig(port, SMTPSecurityImplicitTLS)); err == nil {
			t.Fatal("untrusted SMTP certificate was accepted")
		}
		if capture := waitSMTPFixture(t, result); capture.authBeforeTLS || capture.authAfterTLS {
			t.Fatalf("credentials were sent to an untrusted SMTP server: %#v", capture)
		}
	})
	t.Run("hostname_mismatch", func(t *testing.T) {
		cert, roots := smtpFixtureCertificate(t, []string{"smtp.fixture.test"}, nil)
		port, result := startSMTPFixture(t, SMTPSecurityImplicitTLS, cert)
		sender := NewSender()
		sender.smtpRootCAs = roots
		if err := sendSMTPFixture(t, sender, smtpTestConfig(port, SMTPSecurityImplicitTLS)); err == nil {
			t.Fatal("SMTP certificate with a hostname mismatch was accepted")
		}
		if capture := waitSMTPFixture(t, result); capture.authBeforeTLS || capture.authAfterTLS {
			t.Fatalf("credentials were sent to a hostname-mismatched SMTP server: %#v", capture)
		}
	})
}

func TestSMTPModeValidationAndPersistence(t *testing.T) {
	now := time.Now().UTC()
	store, _ := newTestStore(t, &now)
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{
		Name: "implicit TLS", Kind: ChannelSMTP,
		Config:  ChannelConfig{SMTPHost: "smtp.example.test", SMTPPort: 465, SMTPFrom: "admin@example.test", SMTPTo: "ops@example.test", SMTPSecurityMode: SMTPSecurityImplicitTLS},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.GetChannel(context.Background(), channel.ID)
	if err != nil || reloaded.Config.SMTPSecurityMode != SMTPSecurityImplicitTLS {
		t.Fatalf("SMTP security mode did not persist: %#v err=%v", reloaded.Config, err)
	}

	base := ChannelInput{Name: "SMTP", Kind: ChannelSMTP, Config: smtpTestConfig(465, SMTPSecurityImplicitTLS)}
	base.Config.SMTPUsername = ""
	if err := ValidateChannel(base); err != nil {
		t.Fatalf("valid implicit TLS channel rejected: %v", err)
	}
	base.Config.SMTPSecurityMode = "plaintext"
	if err := ValidateChannel(base); err == nil {
		t.Fatal("unsupported SMTP security mode accepted")
	}
	for name, config := range map[string]ChannelConfig{
		"SMTP host": {WebhookURL: "https://hooks.example.test", SMTPHost: "smtp.example.test"},
		"SMTP mode": {WebhookURL: "https://hooks.example.test", SMTPSecurityMode: SMTPSecuritySTARTTLS},
	} {
		if err := ValidateChannel(ChannelInput{Name: name, Kind: ChannelWebhook, Config: config}); err == nil {
			t.Fatalf("webhook channel accepted SMTP field %s", name)
		}
	}
	webhookConfig := ChannelConfig{WebhookURL: "https://hooks.example.test", SMTPPort: 465}
	if err := ValidateChannel(ChannelInput{Name: "webhook", Kind: ChannelWebhook, Config: webhookConfig}); err == nil {
		t.Fatal("webhook channel accepted SMTP port")
	}
}
