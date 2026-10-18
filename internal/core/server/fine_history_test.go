package server

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestFineHistoryRangeAndResolution(t *testing.T) {
	s := featureCore(t)
	now := time.Now().UTC()
	for _, item := range []struct {
		resolution string
		from, to   time.Time
		want       int
	}{
		{"5s", now.Add(-4 * time.Hour), now, 200},
		{"5s", now.Add(-4*time.Hour - time.Second), now, 400},
		{"5s", now.Add(-25 * time.Hour), now.Add(-24 * time.Hour), 400},
		{"1s", now.Add(-time.Hour), now, 400},
		{"minute", now.Add(-4 * time.Hour), now, 200},
	} {
		q := url.Values{"resolution": {item.resolution}, "from": {item.from.Format(time.RFC3339Nano)}, "to": {item.to.Format(time.RFC3339Nano)}}
		w := httptest.NewRecorder()
		s.handleNodeMetricHistory(w, httptest.NewRequest("GET", "/history?"+q.Encode(), nil), "fixture")
		if w.Code != item.want {
			t.Fatalf("resolution=%s range=%s status=%d body=%s", item.resolution, item.to.Sub(item.from), w.Code, w.Body.String())
		}
	}
}
