package server

import (
	"database/sql"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/feeds"
	"net/http"
	"strings"
)

func (s *Server) feedEvent(r *http.Request, action string) audit.Event {
	return audit.Event{OccurredAt: s.now(), Action: action, Outcome: "succeeded", ActorID: requestActor(r), RemoteAddr: s.effectiveRemoteAddr(r)}
}
func feedError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, sql.ErrNoRows) {
		status = 404
	} else if errors.Is(e, feeds.ErrConflict) {
		status = 409
	}
	http.Error(w, e.Error(), status)
}
func (s *Server) handleFeedAPI(w http.ResponseWriter, r *http.Request) bool {
	const base = "/api/v1/feeds"
	if r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/") {
		return false
	}
	if s.feeds == nil {
		http.Error(w, "subscriptions are unavailable", 503)
		return true
	}
	path := strings.TrimPrefix(r.URL.Path, base)
	if path == "/options" && r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"intervalMinutes": feeds.IntervalChoices, "defaultIntervalMinutes": feeds.DefaultInterval, "minimumIntervalMinutes": 1, "maximumIntervalMinutes": 10080, "maxSubscriptions": 200})
		return true
	}
	if path == "" && r.Method == "GET" {
		v, e := s.feeds.List(r.Context())
		if e != nil {
			feedError(w, e)
		} else {
			writeJSON(w, 200, map[string]any{"subscriptions": v})
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	id := ""
	if path != "" {
		id = parts[0]
		if !validUUID(id) {
			http.NotFound(w, r)
			return true
		}
	}
	if (path == "" && r.Method == "POST") || (len(parts) == 1 && id != "" && r.Method == "PUT") {
		var in feeds.Input
		if !decodeJSON(w, r, &in) {
			return true
		}
		if s.alerts == nil && len(in.ChannelIDs) > 0 {
			http.Error(w, "notifications are unavailable", 503)
			return true
		}
		for _, channelID := range in.ChannelIDs {
			if !validUUID(channelID) {
				http.Error(w, "invalid notification channel", 400)
				return true
			}
			c, e := s.alerts.GetChannel(r.Context(), channelID)
			if e != nil || !c.Enabled {
				http.Error(w, "notification channel must exist and be enabled", 400)
				return true
			}
		}
		v, e := s.feeds.Save(r.Context(), id, in, s.feedEvent(r, "feed_save"))
		if e != nil {
			feedError(w, e)
		} else {
			writeJSON(w, 200, v)
		}
		return true
	}
	if len(parts) == 1 && id != "" {
		switch r.Method {
		case "GET":
			v, e := s.feeds.Get(r.Context(), id)
			if e != nil {
				feedError(w, e)
			} else {
				writeJSON(w, 200, v)
			}
		case "DELETE":
			var in struct {
				Revision int64 `json:"revision"`
				Confirm  bool  `json:"confirm"`
			}
			if !decodeJSON(w, r, &in) {
				return true
			}
			if !in.Confirm {
				http.Error(w, "subscription deletion requires confirmation", 400)
				return true
			}
			e := s.feeds.Delete(r.Context(), id, in.Revision, s.feedEvent(r, "feed_delete"))
			if e != nil {
				feedError(w, e)
			} else {
				w.WriteHeader(204)
			}
		default:
			http.Error(w, "method not allowed", 405)
		}
		return true
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "refresh", "cancel":
			if r.Method != "POST" {
				http.Error(w, "method not allowed", 405)
				return true
			}
			v, e := s.feeds.Action(r.Context(), id, parts[1], s.feedEvent(r, "feed_"+parts[1]))
			if e != nil {
				feedError(w, e)
			} else {
				writeJSON(w, 202, v)
			}
			return true
		case "items":
			if r.Method != "GET" {
				http.Error(w, "method not allowed", 405)
				return true
			}
			offset, e := feeds.ParsePage(r.URL.Query().Get("offset"), 0)
			if e != nil {
				feedError(w, e)
				return true
			}
			limit, e := feeds.ParsePage(r.URL.Query().Get("limit"), 50)
			if e != nil {
				feedError(w, e)
				return true
			}
			unread := r.URL.Query().Get("unread")
			if unread != "" && unread != "true" && unread != "false" {
				http.Error(w, "invalid unread filter", 400)
				return true
			}
			v, e := s.feeds.Items(r.Context(), id, offset, limit, unread == "true", r.URL.Query().Get("search"))
			if e != nil {
				feedError(w, e)
			} else {
				writeJSON(w, 200, map[string]any{"items": v})
			}
			return true
		case "deliveries":
			if r.Method != "GET" {
				http.Error(w, "method not allowed", 405)
				return true
			}
			v, e := s.feeds.Deliveries(r.Context(), id)
			if e != nil {
				feedError(w, e)
			} else {
				writeJSON(w, 200, map[string]any{"deliveries": v})
			}
			return true
		}
	}
	if len(parts) == 3 && parts[1] == "items" && r.Method == "PUT" {
		var in struct {
			Read bool `json:"read"`
		}
		if !decodeJSON(w, r, &in) {
			return true
		}
		e := s.feeds.MarkRead(r.Context(), id, parts[2], in.Read, s.feedEvent(r, "feed_read"))
		if e != nil {
			feedError(w, e)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	if len(parts) == 4 && parts[1] == "deliveries" && parts[3] == "retry" && r.Method == "POST" {
		e := s.feeds.RetryDelivery(r.Context(), id, parts[2], s.feedEvent(r, "feed_delivery_retry"))
		if e != nil {
			feedError(w, e)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	http.NotFound(w, r)
	return true
}
