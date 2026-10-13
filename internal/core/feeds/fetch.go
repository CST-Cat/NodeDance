package feeds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const maxFeedBytes = 4 << 20

var forbidden = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2002::/16", "2001:db8::/32", "3fff::/20", "fc00::/7", "fe80::/10"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range forbidden {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return nil, errors.New("subscription DNS lookup failed")
	}
	if len(ips) == 0 {
		return nil, errors.New("subscription host has no address")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("subscription target is not a public network address")
		}
	}
	var last error
	for _, ip := range ips[:min(len(ips), 16)] {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, last
}
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: safeDial, MaxIdleConns: 8, MaxIdleConnsPerHost: 1, IdleConnTimeout: time.Minute, ResponseHeaderTimeout: 10 * time.Second, TLSHandshakeTimeout: 8 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("subscription redirect limit exceeded")
		}
		_, e := ValidateURL(r.URL.String())
		return e
	}}
}

type fetched struct {
	items          []Item
	etag, modified string
	unchanged      bool
	retry          time.Duration
}

func text(value string, limit int) string {
	z := html.NewTokenizer(strings.NewReader(value))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		t := z.Token()
		if tt == html.StartTagToken && (t.Data == "script" || t.Data == "style") {
			skip++
		}
		if tt == html.EndTagToken && (t.Data == "script" || t.Data == "style") && skip > 0 {
			skip--
			continue
		}
		if tt == html.TextToken && skip == 0 {
			b.WriteString(t.Data)
			b.WriteByte(' ')
			if b.Len() > limit*2 {
				break
			}
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit])
	}
	return s
}
func (s *Store) fetch(ctx context.Context, sub Subscription, etag, modified string) (fetched, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", sub.URL, nil)
	if e != nil {
		return fetched{}, e
	}
	req.Header.Set("User-Agent", "NodeDance RSS reader")
	req.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml, text/xml")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	res, e := s.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return fetched{}, ctx.Err()
		}
		return fetched{}, errors.New("subscription fetch failed")
	}
	defer res.Body.Close()
	result := fetched{etag: res.Header.Get("ETag"), modified: res.Header.Get("Last-Modified")}
	if len(result.etag) > 1024 || len(result.modified) > 1024 {
		return result, errors.New("subscription validator exceeds limit")
	}
	if res.StatusCode == 304 {
		result.unchanged = true
		return result, nil
	}
	if res.StatusCode != 200 {
		if n, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && n > 0 {
			result.retry = time.Duration(min(n, 86400)) * time.Second
		} else if at, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			result.retry = max(0, min(at.Sub(s.now()), 24*time.Hour))
		}
		return result, errors.New("subscription server returned HTTP " + strconv.Itoa(res.StatusCode))
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, maxFeedBytes+1))
	if e != nil {
		return result, e
	}
	if len(raw) > maxFeedBytes {
		return result, errors.New("subscription exceeds 4 MiB")
	}
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	decoder.CharsetReader = charset.NewReaderLabel
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, errors.New("subscription XML is invalid")
		}
		switch token.(type) {
		case xml.Directive:
			return result, errors.New("subscription XML declarations are not allowed")
		case xml.StartElement:
			depth++
			if depth > 64 {
				return result, errors.New("subscription XML nesting exceeds limit")
			}
		case xml.EndElement:
			depth--
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	parsed, e := gofeed.NewParser().Parse(strings.NewReader(string(raw)))
	if e != nil {
		return result, errors.New("subscription XML could not be parsed")
	}
	if parsed.FeedType != "rss" && parsed.FeedType != "atom" {
		return result, errors.New("subscription must be RSS or Atom")
	}
	if len(parsed.Items) > 1000 {
		return result, errors.New("subscription exceeds 1000 entries")
	}
	for _, entry := range parsed.Items {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		title, summary := text(entry.Title, 300), text(entry.Description, 2000)
		if summary == "" {
			summary = text(entry.Content, 2000)
		}
		if title == "" && summary == "" {
			continue
		}
		link := ""
		if u, e := res.Request.URL.Parse(entry.Link); e == nil && entry.Link != "" && len(u.String()) <= 2048 && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") {
			link = u.String()
		}
		key := entry.GUID
		if key == "" {
			key = link
		}
		if key == "" {
			key = title + "\x00" + entry.Published + "\x00" + summary
		}
		hash := sha256.Sum256([]byte(sub.ID + "\x00" + key))
		result.items = append(result.items, Item{ID: hex.EncodeToString(hash[:]), FeedID: sub.ID, Title: title, Summary: summary, URL: link, Published: entry.PublishedParsed})
	}
	return result, nil
}
