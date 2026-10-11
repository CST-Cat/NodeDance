package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type httpAgentChannel struct {
	ctx         context.Context
	cancel      context.CancelFunc
	client      *http.Client
	endpoint    string
	credential  string
	compression bool
	session     string
	readSeq     uint64
	writeSeq    uint64
	writeMu     sync.Mutex
}

func newHTTPAgentChannel(ctx context.Context, client *http.Client, server, credential string, compression bool) *httpAgentChannel {
	ctx, cancel := context.WithCancel(ctx)
	return &httpAgentChannel{ctx: ctx, cancel: cancel, client: client, endpoint: strings.TrimSuffix(server, "/") + "/api/v1/agents/channel", credential: credential, compression: compression}
}
func (c *httpAgentChannel) SetReadLimit(int64)                       {}
func (c *httpAgentChannel) Close(websocket.StatusCode, string) error { return c.CloseNow() }
func (c *httpAgentChannel) CloseNow() error                          { c.cancel(); return nil }
func (c *httpAgentChannel) request(ctx context.Context, method, endpoint string, data []byte) (*http.Response, error) {
	var body io.Reader = bytes.NewReader(data)
	compressed := false
	if c.compression && len(data) > 512 {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		_, _ = gz.Write(data)
		_ = gz.Close()
		body = &b
		compressed = true
	}
	reqCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	req, e := http.NewRequestWithContext(reqCtx, method, endpoint, body)
	if e != nil {
		stop()
		cancel()
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("Content-Type", "application/json")
	if method == "GET" {
		req.Header.Set("X-Channel-Ack", strconv.FormatUint(c.readSeq, 10))
	}
	if method == "POST" {
		req.Header.Set("X-Channel-Sequence", strconv.FormatUint(c.writeSeq+1, 10))
	}
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	resp.Body = &channelResponseBody{ReadCloser: resp.Body, close: func() { stop(); cancel() }}
	return resp, nil
}
func (c *httpAgentChannel) Write(ctx context.Context, _ websocket.MessageType, b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	endpoint := c.endpoint
	if c.session != "" {
		endpoint += "/" + c.session
	}
	for attempt := 0; attempt < 2; attempt++ {
		resp, e := c.request(ctx, "POST", endpoint, b)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if resp.StatusCode == 201 && c.session == "" {
			var result struct {
				ChannelID string `json:"channelId"`
			}
			e = json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&result)
			resp.Body.Close()
			if e != nil || !isUUID(result.ChannelID) {
				return errors.New("invalid HTTP channel")
			}
			c.session = result.ChannelID
			return nil
		}
		resp.Body.Close()
		if resp.StatusCode == 204 {
			c.writeSeq++
			return nil
		}
		if resp.StatusCode < 500 {
			return errors.New("HTTP Agent write rejected")
		}
	}
	return errors.New("HTTP Agent write unavailable")
}
func (c *httpAgentChannel) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	for {
		select {
		case <-c.ctx.Done():
			return 0, nil, c.ctx.Err()
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		default:
		}
		reqCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		resp, e := c.request(reqCtx, "GET", c.endpoint+"/"+c.session, nil)
		if e != nil {
			cancel()
			return 0, nil, errors.New("HTTP Agent polling failed")
		}
		if resp.StatusCode == 204 {
			resp.Body.Close()
			cancel()
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			cancel()
			return 0, nil, errors.New("HTTP Agent channel unavailable")
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxMessageBytes+1))
		seq, seqErr := strconv.ParseUint(resp.Header.Get("X-Channel-Sequence"), 10, 64)
		resp.Body.Close()
		cancel()
		if e != nil || seqErr != nil || len(b) > protocol.MaxMessageBytes || seq != c.readSeq+1 {
			return 0, nil, errors.New("invalid HTTP Agent frame")
		}
		c.readSeq = seq
		return websocket.MessageText, b, nil
	}
}

type channelResponseBody struct {
	io.ReadCloser
	close func()
}

func (b *channelResponseBody) Close() error { err := b.ReadCloser.Close(); b.close(); return err }
