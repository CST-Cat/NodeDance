// Package geolocation resolves only public node IPs. No browser calls a third party.
package geolocation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Country struct {
	Position  *Position `json:"-"`
	Status    string    `json:"status"`
	Code      string    `json:"code,omitempty"`
	Name      string    `json:"name,omitempty"`
	Source    string    `json:"source,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	SampledAt string    `json:"sampledAt,omitempty"`
}
type entry struct {
	country Country
	expires time.Time
}
type Resolver struct {
	mu       sync.Mutex
	entries  map[string]*entry
	slots    chan struct{}
	client   *http.Client
	window   time.Time
	requests int
}

func New() *Resolver {
	return &Resolver{entries: make(map[string]*entry), slots: make(chan struct{}, 4), client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001:2::/48"),
}

func PublicIP(value string) string {
	ip, err := netip.ParseAddr(value)
	if err != nil || ip.Zone() != "" {
		return ""
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return ""
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return ""
		}
	}
	return ip.String()
}

// Lookup never waits for an external request. Cached results are shared across
// nodes, bounded to 1,000 IPs and 1,000 requests per rolling day, with four workers.
func (r *Resolver) Lookup(value string) Country {
	ip := PublicIP(value)
	if ip == "" {
		return Country{Status: "unknown", Reason: "public_ip_unavailable"}
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached := r.entries[ip]; cached != nil && now.Before(cached.expires) {
		return cached.country
	}
	if now.Sub(r.window) >= 24*time.Hour {
		r.window = now
		r.requests = 0
	}
	if r.requests >= 1000 {
		return Country{Status: "unknown", Reason: "lookup_limit"}
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return Country{Status: "unknown", Reason: "lookup_pending"}
	}
	if len(r.entries) >= 1000 {
		var oldest string
		var expires time.Time
		for key, value := range r.entries {
			if oldest == "" || value.expires.Before(expires) {
				oldest = key
				expires = value.expires
			}
		}
		delete(r.entries, oldest)
	}
	pending := &entry{country: Country{Status: "unknown", Reason: "lookup_pending"}, expires: now.Add(time.Minute)}
	r.entries[ip] = pending
	r.requests++
	go r.resolve(ip, pending)
	return pending.country
}
func (r *Resolver) resolve(ip string, pending *entry) {
	defer func() { <-r.slots }()
	country := Country{Status: "unknown", Reason: "lookup_unavailable"}
	ttl := 15 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip+"?fields=ip,success,country,country_code,city,region,latitude,longitude", nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "NodeDance")
	response, err := r.client.Do(request)
	if err == nil {
		defer response.Body.Close()
		var result struct {
			IP        string   `json:"ip"`
			City      string   `json:"city"`
			Region    string   `json:"region"`
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
			Success   bool     `json:"success"`
			Code      string   `json:"country_code"`
			Name      string   `json:"country"`
		}
		if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result) == nil && result.Success && PublicIP(result.IP) == ip && len(result.Code) == 2 && result.Code[0] >= 'A' && result.Code[0] <= 'Z' && result.Code[1] >= 'A' && result.Code[1] <= 'Z' && len(result.Name) <= 100 && strings.TrimSpace(result.Name) != "" {
			country = Country{Status: "known", Code: result.Code, Name: result.Name, Source: "ipwho.is", SampledAt: time.Now().UTC().Format(time.RFC3339)}
			point := &Position{CountryCode: result.Code, City: strings.TrimSpace(result.City), Region: strings.TrimSpace(result.Region), Latitude: result.Latitude, Longitude: result.Longitude}
			if ValidPosition(point) {
				country.Position = point
			}
			ttl = 24 * time.Hour
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[ip] == pending {
		pending.country = country
		pending.expires = time.Now().Add(ttl)
	}
}
