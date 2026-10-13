// Package feeds owns Core-local RSS and Atom subscriptions. It never dispatches Agent work.
package feeds

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

const DefaultInterval = 30

var IntervalChoices = []int{1, 5, 10, 15, 30, 60, 180, 360, 720, 1440, 10080}
var ErrConflict = errors.New("subscription changed or no longer exists")

type Input struct {
	Name             string   `json:"name"`
	URL              string   `json:"url"`
	IntervalMinutes  int      `json:"intervalMinutes,omitempty"`
	Enabled          bool     `json:"enabled"`
	Keywords         []string `json:"keywords"`
	ChannelIDs       []string `json:"channelIds"`
	NotificationMode string   `json:"notificationMode"`
	DigestMinutes    int      `json:"digestMinutes,omitempty"`
	Revision         int64    `json:"revision,omitempty"`
}
type Subscription struct {
	Input
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	RunID       string    `json:"runId"`
	Error       string    `json:"error"`
	LastChecked time.Time `json:"lastChecked"`
	LastSuccess time.Time `json:"lastSuccess"`
	NextCheck   time.Time `json:"nextCheck"`
	Failures    int       `json:"failures"`
}
type Item struct {
	ID        string     `json:"id"`
	FeedID    string     `json:"feedId"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary"`
	URL       string     `json:"url"`
	Published *time.Time `json:"published,omitempty"`
	FirstSeen time.Time  `json:"firstSeen"`
	Read      bool       `json:"read"`
}
type Delivery struct {
	ID        string    `json:"id"`
	FeedID    string    `json:"feedId"`
	ChannelID string    `json:"channelId"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error"`
	Created   time.Time `json:"created"`
}

func ValidateURL(value string) (*url.URL, error) {
	u, e := url.Parse(value)
	if e != nil || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("subscription URL must be an HTTP or HTTPS URL without credentials or fragment")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return nil, errors.New("subscription URL must use port 80 or 443")
	}
	return u, nil
}
func validate(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		return errors.New("subscription name must contain 1 to 120 bytes")
	}
	if _, e := ValidateURL(in.URL); e != nil {
		return e
	}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = DefaultInterval
	}
	if in.DigestMinutes == 0 {
		in.DigestMinutes = 60
	}
	if in.IntervalMinutes < 1 || in.IntervalMinutes > 10080 || in.DigestMinutes < 1 || in.DigestMinutes > 10080 {
		return errors.New("subscription intervals must be 1 to 10080 minutes")
	}
	if in.NotificationMode != "off" && in.NotificationMode != "immediate" && in.NotificationMode != "digest" {
		return errors.New("notificationMode must be off, immediate or digest")
	}
	if len(in.Keywords) > 20 || len(in.ChannelIDs) > 8 {
		return errors.New("too many subscription keywords or notification channels")
	}
	if in.NotificationMode != "off" && len(in.ChannelIDs) == 0 {
		return errors.New("subscription notifications require at least one enabled channel")
	}
	seen := map[string]bool{}
	for _, id := range in.ChannelIDs {
		if seen[id] {
			return errors.New("duplicate notification channel")
		}
		seen[id] = true
	}
	for _, k := range in.Keywords {
		if strings.TrimSpace(k) == "" || len(k) > 120 {
			return errors.New("subscription keyword must contain 1 to 120 bytes")
		}
	}
	return nil
}
