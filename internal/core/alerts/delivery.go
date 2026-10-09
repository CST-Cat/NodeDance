package alerts

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
	"unicode"
)

type Sender struct {
	HTTPClient *http.Client
	Timeout    time.Duration
}

func NewSender() *Sender {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	return &Sender{HTTPClient: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Timeout: 8 * time.Second}
}

func (s *Sender) Send(ctx context.Context, channel Channel, secret, payload string) (int, error) {
	if s == nil {
		s = NewSender()
	}
	switch channel.Kind {
	case ChannelWebhook:
		return s.sendWebhook(ctx, channel.Config.WebhookURL, secret, payload)
	case ChannelSMTP:
		return 0, s.sendSMTP(ctx, channel.Config, secret, payload)
	default:
		return 0, errors.New("unsupported notification channel")
	}
}

func (s *Sender) sendWebhook(ctx context.Context, target, secret, payload string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(payload))
	if err != nil {
		return 0, errors.New("invalid webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NodeDance/1.0 alerts")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := s.HTTPClient
	if client == nil {
		client = NewSender().HTTPClient
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, errors.New("webhook request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func (s *Sender) sendSMTP(ctx context.Context, config ChannelConfig, secret, payload string) error {
	addr := net.JoinHostPort(config.SMTPHost, fmt.Sprint(config.SMTPPort))
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return errors.New("SMTP connection setup failed")
	}
	client, err := smtp.NewClient(conn, config.SMTPHost)
	if err != nil {
		return errors.New("SMTP server handshake failed")
	}
	defer client.Close()
	tlsActive := false
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.SMTPHost}); err != nil {
			return errors.New("SMTP TLS negotiation failed")
		}
		tlsActive = true
	}
	if !tlsActive && !isLoopbackHost(config.SMTPHost) {
		return errors.New("remote SMTP requires STARTTLS")
	}
	if config.SMTPUsername != "" || secret != "" {
		if !tlsActive && !isLoopbackHost(config.SMTPHost) {
			return errors.New("SMTP authentication requires TLS")
		}
		if config.SMTPUsername == "" || secret == "" {
			return errors.New("SMTP username and password must be provided together")
		}
		if err := client.Auth(smtp.PlainAuth("", config.SMTPUsername, secret, config.SMTPHost)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	from, err := mail.ParseAddress(config.SMTPFrom)
	if err != nil {
		return errors.New("invalid SMTP sender address")
	}
	to, err := mail.ParseAddress(config.SMTPTo)
	if err != nil {
		return errors.New("invalid SMTP recipient address")
	}
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender was rejected")
	}
	if err := client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP recipient was rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message was rejected")
	}
	body, err := smtpNotificationText(payload)
	if err != nil {
		return errors.New("SMTP notification payload is invalid")
	}
	message := "From: " + from.String() + "\r\nTo: " + to.String() + "\r\nSubject: NodeDance alert notification\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n"
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return errors.New("SMTP message write failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message delivery failed")
	}
	if err := client.Quit(); err != nil {
		return errors.New("SMTP server did not confirm delivery")
	}
	return nil
}

func smtpNotificationText(payload string) (string, error) {
	var notification Notification
	if err := json.Unmarshal([]byte(payload), &notification); err != nil {
		return "", err
	}
	lines := []string{
		"Event: " + escapeSMTPText(notification.Event),
		"Alert ID: " + escapeSMTPText(notification.AlertID),
		"Rule: " + escapeSMTPText(notification.RuleName),
		"Node: " + escapeSMTPText(notification.NodeName),
		"Node ID: " + escapeSMTPText(notification.NodeID),
		"Subject ID: " + escapeSMTPText(notification.SubjectID),
		"Severity: " + escapeSMTPText(notification.Severity),
		"Occurred at: " + escapeSMTPText(notification.OccurredAt.UTC().Format(time.RFC3339Nano)),
		"",
		"Message:",
		escapeSMTPText(notification.Message),
	}
	return strings.Join(lines, "\r\n"), nil
}

// SMTP values are plain-text body content. Escape line breaks and control
// characters so an untrusted notification value cannot mimic another header
// or add SMTP DATA commands; ordinary Unicode text remains readable.
func escapeSMTPText(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch {
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\t':
			out.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
