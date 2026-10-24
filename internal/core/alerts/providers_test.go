package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

type providerTransport func(*http.Request) (*http.Response, error)

func (f providerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderRequestsAndAcknowledgements(t *testing.T) {
	cases := []struct{ provider, secret, reply string }{
		{"telegram", "123:test_token", `{"ok":true}`},
		{"bark", "device-private", `{"code":200}`},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=private", `{"errcode":0}`},
		{"feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/private", `{"code":0}`},
		{"wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=private", `{"errcode":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			c := Channel{Kind: ChannelWebhook, Config: ChannelConfig{Provider: tc.provider}}
			if tc.provider == "telegram" {
				c.Config.ChatID = "-100123"
			}
			if tc.provider == "dingtalk" || tc.provider == "feishu" {
				c.credentials.SigningSecret = "sign-private"
			}
			if err := ValidateChannel(ChannelInput{Name: "fixture", Kind: c.Kind, Config: c.Config}); err != nil {
				t.Fatal(err)
			}
			if err := validateProviderSecret(c.Config, tc.secret, c.credentials); err != nil {
				t.Fatal(err)
			}
			reply := tc.reply
			sender := NewSender()
			sender.HTTPClient.Transport = providerTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "" {
					t.Fatal("unexpected transport or leaked bearer")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if json.Unmarshal(raw, &body) != nil {
					t.Fatal("invalid request JSON")
				}
				switch tc.provider {
				case "telegram":
					if body["chat_id"] != "-100123" || !strings.Contains(r.URL.Path, "123:test_token/sendMessage") || !strings.Contains(body["text"].(string), `quote \" and`) {
						t.Fatalf("bad Telegram request: %s %s", r.URL, raw)
					}
				case "bark":
					if r.URL.String() != "https://api.day.app/push" || body["device_key"] != tc.secret || body["body"] == nil {
						t.Fatal("bad Bark request")
					}
				case "dingtalk":
					if r.URL.Query().Get("sign") == "" || r.URL.Query().Get("timestamp") == "" || body["msgtype"] != "text" {
						t.Fatal("missing DingTalk signature")
					}
				case "feishu":
					if body["sign"] == nil || body["timestamp"] == nil || body["msg_type"] != "text" {
						t.Fatal("missing Feishu signature")
					}
				case "wecom":
					if body["msgtype"] != "text" {
						t.Fatal("bad WeCom format")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})
			payload := `{"event":"test","message":"quote \\\" and 中文","occurredAt":"2026-01-01T00:00:00Z"}`
			if status, err := sender.Send(context.Background(), c, tc.secret, payload); err != nil || status != 200 {
				t.Fatalf("send: %d %v", status, err)
			}
			for _, bad := range []string{`{"ok":false,"code":1,"errcode":1,"message":"private"}`, `{}`, `{"ok":null,"code":null,"errcode":null}`, `invalid`} {
				reply = bad
				_, err := sender.Send(context.Background(), c, tc.secret, payload)
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatalf("unsafe acknowledgement handling: %v", err)
				}
			}
		})
	}
}

func TestCustomWebhookEscapingHeadersAndRedirects(t *testing.T) {
	var sent int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		if r.Method != "PATCH" || r.Header.Get("Authorization") != "custom-private" {
			t.Error("missing custom request settings")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["text"] != "\"},\"evil\":true" {
			t.Error("unsafe template substitution")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Channel{Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: server.URL, Method: "PATCH", BodyTemplate: `{"text":"{{message}}"}`}, credentials: channelCredentials{Headers: map[string]string{"Authorization": "custom-private"}}}
	if _, err := NewSender().Send(context.Background(), c, "", `{"event":"test","message":"\"},\"evil\":true"}`); err != nil {
		t.Fatal(err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, server.URL, 307) }))
	defer redirect.Close()
	c.Config.WebhookURL = redirect.URL
	sender := NewSender()
	sender.HTTPClient.CheckRedirect = nil
	if status, err := sender.Send(context.Background(), c, "", `{"event":"test","message":"\"},\"evil\":true"}`); err == nil || status != 307 || sent != 1 {
		t.Fatal("followed credential-bearing redirect")
	}
	for _, headers := range []map[string]string{{"Cookie": "private"}, {"X-Test": "x\r\nInjected:true"}, {"authorization": "x", "Authorization": "y"}} {
		if validateHeaders(headers) == nil {
			t.Fatal("accepted unsafe headers")
		}
	}
	if _, err := renderWebhookBody(`{"text":"{{unknown}}"}`, `{}`); err == nil {
		t.Fatal("accepted unknown template variable")
	}
	if err := validateProviderSecret(ChannelConfig{}, "bearer", c.credentials); err == nil {
		t.Fatal("accepted conflicting authorization")
	}
}

func TestChannelCredentialsEncryptedPreservedAndUsedByQueues(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db.DB, make([]byte, 32), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	input := ChannelInput{Name: "fixture", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: "https://example.invalid/hook"}, Headers: map[string]string{"Authorization": "headers-private"}, Enabled: true}
	c, err := store.SaveChannel(ctx, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasHeaders {
		t.Fatal("missing redacted credential indicator")
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "headers-private") {
		t.Fatal("credentials escaped response")
	}
	var encrypted []byte
	if err = db.DB.QueryRow(`SELECT ciphertext FROM alert_channel_credentials WHERE channel_id=?`, c.ID).Scan(&encrypted); err != nil || strings.Contains(string(encrypted), "headers-private") {
		t.Fatal("credentials not encrypted")
	}
	input.Headers = nil
	input.Revision = c.Revision
	c, err = store.SaveChannel(ctx, c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasHeaders {
		t.Fatal("blank edit discarded credentials")
	}
	runtime, _, err := store.NotificationChannel(ctx, c.ID)
	if err != nil || runtime.credentials.Headers["Authorization"] != "headers-private" {
		t.Fatal("shared producers lost headers")
	}
	if err := store.EnqueueTest(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	_, claimed, _, ok, err := store.ClaimDelivery(ctx, time.Now())
	if err != nil || !ok || claimed.credentials.Headers["Authorization"] != "headers-private" {
		t.Fatalf("alert delivery lost headers: %v", err)
	}
	input.Config = ChannelConfig{Provider: "telegram", ChatID: "123"}
	input.Revision = c.Revision
	if _, err = store.SaveChannel(ctx, c.ID, input); err == nil {
		t.Fatal("provider switch reused credentials")
	}
	input.Secret = "123:token_private"
	c, err = store.SaveChannel(ctx, c.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if c.HasHeaders {
		t.Fatal("provider switch retained custom headers")
	}
	input.Revision = c.Revision - 1
	if _, err = store.SaveChannel(ctx, c.ID, input); err == nil {
		t.Fatal("stale revision accepted")
	}
	input.Revision = c.Revision
	input.Secret = ""
	c, err = store.SaveChannel(ctx, c.ID, input)
	if err != nil || !c.HasSecret {
		t.Fatal("secret not retained")
	}
}

func TestProvidersKeepSubscriptionAndCertificateContent(t *testing.T) {
	payloads := []struct{ raw, want string }{
		{`{"event":"feed_digest","feedName":"Updates","items":[{"title":"New release","url":"https://example.invalid/release"}]}`, "https://example.invalid/release"},
		{`{"event":"certificate_expiry","message":"Certificate example.invalid expires tomorrow","occurredAt":"2026-01-01T00:00:00Z"}`, "Certificate example.invalid expires tomorrow"},
	}
	for _, payload := range payloads {
		sender := NewSender()
		sender.HTTPClient.Transport = providerTransport(func(r *http.Request) (*http.Response, error) {
			var body struct {
				Text string `json:"text"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(body.Text, payload.want) {
				t.Fatalf("shared provider lost content: %q", body.Text)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
		})
		if _, err := sender.Send(context.Background(), Channel{Kind: ChannelWebhook, Config: ChannelConfig{Provider: "telegram", ChatID: "123"}}, "123:token", payload.raw); err != nil {
			t.Fatal(err)
		}
	}
}
