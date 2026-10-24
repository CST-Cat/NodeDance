package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ChannelProvider struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	SecretLabel   string   `json:"secretLabel"`
	URL           bool     `json:"url"`
	ChatID        bool     `json:"chatId"`
	Signing       bool     `json:"signing"`
	CustomRequest bool     `json:"customRequest"`
	Methods       []string `json:"methods,omitempty"`
}

func ChannelProviders() []ChannelProvider {
	return []ChannelProvider{
		{ID: "", Name: "Webhook", SecretLabel: "Bearer token", URL: true, CustomRequest: true, Methods: []string{"POST", "PUT", "PATCH"}},
		{ID: "telegram", Name: "Telegram", SecretLabel: "Bot token", ChatID: true},
		{ID: "bark", Name: "Bark", SecretLabel: "Device key", URL: true},
		{ID: "dingtalk", Name: "DingTalk", SecretLabel: "Webhook URL", Signing: true},
		{ID: "feishu", Name: "Feishu / Lark", SecretLabel: "Webhook URL", Signing: true},
		{ID: "wecom", Name: "WeCom", SecretLabel: "Webhook URL"},
	}
}

type channelCredentials struct {
	Headers       map[string]string `json:"headers,omitempty"`
	SigningSecret string            `json:"signingSecret,omitempty"`
}

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
var botToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func validateWebhookConfig(in ChannelInput) error {
	c := in.Config
	known := false
	for _, p := range ChannelProviders() {
		if p.ID == c.Provider {
			known = true
		}
	}
	if !known {
		return errors.New("unsupported notification provider")
	}
	if c.Provider == "" || c.Provider == "bark" && c.WebhookURL != "" {
		u, e := url.ParseRequestURI(c.WebhookURL)
		if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && (c.Provider != "" || u.Scheme != "http")) {
			return errors.New("webhook URL must be an HTTP(S) URL without embedded credentials")
		}
		if c.Provider == "bark" && (u.RawQuery != "" || u.Path != "" && u.Path != "/") {
			return errors.New("Bark server must be an HTTPS origin")
		}
	} else if c.WebhookURL != "" {
		return errors.New("this provider stores its endpoint in the encrypted secret")
	}
	if c.Provider == "telegram" && (strings.TrimSpace(c.ChatID) == "" || len(c.ChatID) > 200 || strings.ContainsAny(c.ChatID, "\r\n\x00")) {
		return errors.New("Telegram chat ID is required")
	}
	if c.Provider != "telegram" && c.ChatID != "" {
		return errors.New("chat ID is only supported by Telegram")
	}
	if c.Provider != "" && (c.Method != "" || c.BodyTemplate != "" || len(in.Headers) > 0) {
		return errors.New("custom requests are only supported by generic Webhook")
	}
	if c.Method != "" && c.Method != "POST" && c.Method != "PUT" && c.Method != "PATCH" {
		return errors.New("webhook method must be POST, PUT or PATCH")
	}
	if in.SigningSecret != nil && *in.SigningSecret != "" && c.Provider != "dingtalk" && c.Provider != "feishu" {
		return errors.New("signing is only supported by DingTalk and Feishu")
	}
	if in.SigningSecret != nil && (len(*in.SigningSecret) > 4096 || strings.ContainsAny(*in.SigningSecret, "\r\n\x00")) {
		return errors.New("invalid signing secret")
	}
	if err := validateHeaders(in.Headers); err != nil {
		return err
	}
	if c.BodyTemplate != "" {
		if _, err := renderWebhookBody(c.BodyTemplate, `{}`); err != nil {
			return err
		}
	}
	return nil
}

func validateHeaders(headers map[string]string) error {
	if len(headers) > 20 {
		return errors.New("at most 20 custom headers are allowed")
	}
	seen := map[string]bool{}
	for k, v := range headers {
		key := textproto.CanonicalMIMEHeaderKey(k)
		if !headerName.MatchString(k) || len(k) > 100 || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") || seen[key] {
			return errors.New("invalid or duplicate webhook header")
		}
		seen[key] = true
		switch key {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Upgrade", "Trailer", "Te", "Cookie", "Proxy-Authorization", "Proxy-Connection":
			return errors.New("transport and cookie headers cannot be customized")
		}
	}
	return nil
}

func validateProviderSecret(c ChannelConfig, secret string, credentials channelCredentials) error {
	if c.Provider != "" && strings.TrimSpace(secret) == "" {
		return errors.New("provider credentials are required")
	}
	if c.Provider == "telegram" && !botToken.MatchString(secret) {
		return errors.New("invalid Telegram bot token")
	}
	if c.Provider == "bark" && strings.ContainsAny(secret, "\r\n\x00") {
		return errors.New("invalid Bark device key")
	}
	if c.Provider == "" && secret != "" {
		if strings.ContainsAny(secret, "\r\n\x00") {
			return errors.New("invalid Bearer token")
		}
		for k := range credentials.Headers {
			if strings.EqualFold(k, "Authorization") {
				return errors.New("choose either Bearer token or a custom Authorization header")
			}
		}
	}
	if c.Provider == "dingtalk" || c.Provider == "feishu" || c.Provider == "wecom" {
		u, e := url.ParseRequestURI(secret)
		if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
			return errors.New("invalid provider webhook URL")
		}
		valid := false
		switch c.Provider {
		case "dingtalk":
			valid = u.Host == "oapi.dingtalk.com" && u.Path == "/robot/send" && u.Query().Get("access_token") != ""
		case "feishu":
			valid = (u.Host == "open.feishu.cn" || u.Host == "open.larksuite.com") && strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") && len(strings.TrimPrefix(u.Path, "/open-apis/bot/v2/hook/")) > 0
		case "wecom":
			valid = u.Host == "qyapi.weixin.qq.com" && u.Path == "/cgi-bin/webhook/send" && u.Query().Get("key") != ""
		}
		if !valid {
			return errors.New("webhook URL does not match the selected provider")
		}
	}
	return nil
}

func renderWebhookBody(template, payload string) (string, error) {
	if len(template) > 16384 {
		return "", errors.New("webhook body template exceeds 16384 bytes")
	}
	dec := json.NewDecoder(strings.NewReader(template))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return "", errors.New("webhook body template must be valid JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", errors.New("webhook body template must contain one JSON value")
	}
	var n Notification
	if err := json.Unmarshal([]byte(payload), &n); err != nil {
		return "", errors.New("invalid notification payload")
	}
	if n.Message == "" {
		n.Message, _ = smtpNotificationText(payload)
	}
	count := 0
	var visit func(any, int) (any, error)
	visit = func(v any, depth int) (any, error) {
		count++
		if depth > 24 || count > 1024 {
			return nil, errors.New("webhook template is too complex")
		}
		switch x := v.(type) {
		case string:
			if err := ValidateMessageTemplate(x); err != nil {
				return nil, err
			}
			return RenderMessageTemplate(x, n)
		case []any:
			for i := range x {
				v, e := visit(x[i], depth+1)
				if e != nil {
					return nil, e
				}
				x[i] = v
			}
		case map[string]any:
			for k := range x {
				if strings.Contains(k, "{{") {
					return nil, errors.New("webhook template keys cannot contain placeholders")
				}
				v, e := visit(x[k], depth+1)
				if e != nil {
					return nil, e
				}
				x[k] = v
			}
		}
		return v, nil
	}
	v, e := visit(root, 0)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(v)
	return string(b), e
}

func truncateText(s string, limit int) string {
	units := 0
	for i, r := range s {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > limit-1 {
			return s[:i] + "…"
		}
		units += size
	}
	return s
}

func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

func (s *Sender) sendProvider(ctx context.Context, c Channel, secret, payload string) (int, error) {
	config := c.Config
	target := config.WebhookURL
	method := config.Method
	if method == "" {
		method = http.MethodPost
	}
	body := payload
	text, textErr := smtpNotificationText(payload)
	if textErr != nil && config.Provider != "" {
		return 0, textErr
	}
	var value any
	switch config.Provider {
	case "":
		if config.BodyTemplate != "" {
			var err error
			body, err = renderWebhookBody(config.BodyTemplate, payload)
			if err != nil {
				return 0, err
			}
		}
	case "telegram":
		target = "https://api.telegram.org/bot" + secret + "/sendMessage"
		value = map[string]any{"chat_id": config.ChatID, "text": truncateText(text, 4096)}
	case "bark":
		target = strings.TrimRight(config.WebhookURL, "/")
		if target == "" {
			target = "https://api.day.app"
		}
		target += "/push"
		value = map[string]any{"device_key": secret, "title": "NodeDance", "body": truncateText(text, 4096)}
	case "dingtalk", "wecom":
		target = secret
		content := truncateText(text, 4096)
		if config.Provider == "wecom" {
			content = truncateBytes(content, 2048)
		}
		value = map[string]any{"msgtype": "text", "text": map[string]string{"content": content}}
	case "feishu":
		target = secret
		value = map[string]any{"msg_type": "text", "content": map[string]string{"text": truncateText(text, 4096)}}
	default:
		return 0, errors.New("unsupported notification provider")
	}
	if signing := c.credentials.SigningSecret; signing != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		if config.Provider == "dingtalk" {
			timestamp = strconv.FormatInt(time.Now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(signing))
			mac.Write([]byte(timestamp + "\n" + signing))
			u, e := url.Parse(target)
			if e != nil {
				return 0, errors.New("invalid provider endpoint")
			}
			q := u.Query()
			q.Set("timestamp", timestamp)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = q.Encode()
			target = u.String()
		} else if config.Provider == "feishu" {
			mac := hmac.New(sha256.New, []byte(timestamp+"\n"+signing))
			v := value.(map[string]any)
			v["timestamp"] = timestamp
			v["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	}
	if value != nil {
		b, e := json.Marshal(value)
		if e != nil {
			return 0, e
		}
		body = string(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NodeDance notifications")
	for k, v := range c.credentials.Headers {
		req.Header.Set(k, v)
	}
	if config.Provider == "" && secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := s.HTTPClient
	if client == nil {
		client = NewSender().HTTPClient
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return 0, errors.New("webhook request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		return response.StatusCode, errors.New("webhook response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	if config.Provider == "" {
		return response.StatusCode, nil
	}
	if len(raw) > 65536 || !utf8.Valid(raw) {
		return response.StatusCode, errors.New("invalid provider response")
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &result) != nil {
		return response.StatusCode, errors.New("invalid provider response")
	}
	success := false
	if config.Provider == "telegram" {
		var ok bool
		_ = json.Unmarshal(result["ok"], &ok)
		success = ok
	} else {
		key := "code"
		expected := 0
		if config.Provider == "dingtalk" || config.Provider == "wecom" {
			key = "errcode"
		}
		if config.Provider == "bark" {
			expected = 200
		}
		var code *int
		if rawCode, ok := result[key]; ok && json.Unmarshal(rawCode, &code) == nil && code != nil {
			success = *code == expected
		}
	}
	if !success {
		return response.StatusCode, errors.New("notification provider rejected the message; check credentials and robot security settings")
	}
	return response.StatusCode, nil
}
