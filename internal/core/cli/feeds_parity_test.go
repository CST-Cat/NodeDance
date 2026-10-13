package cli

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/core/server"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedsCLIAndTUIDiscoverRealCore(t *testing.T) {
	core, e := server.New("test", server.Options{DataDir: t.TempDir(), Development: true})
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close()
	if e = core.BootstrapAdministrator(context.Background(), "rss-admin", "rss-password-strong"); e != nil {
		t.Fatal(e)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	state := filepath.Join(t.TempDir(), "cli")
	invoke := func(args []string, body string) string {
		t.Helper()
		var out strings.Builder
		args = append(args, "--server", host.URL, "--dev", "--state-dir", state)
		if e := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader(body), &out, io.Discard, "test"); e != nil {
			t.Fatal(e, out.String())
		}
		return out.String()
	}
	invoke([]string{"auth", "login", "--body", "-"}, `{"username":"rss-admin","password":"rss-password-strong"}`)
	body := `{"name":"CLI RSS","url":"https://example.invalid/rss","enabled":false,"intervalMinutes":1,"keywords":[],"channelIds":[],"notificationMode":"off"}`
	out := invoke([]string{"call", "feeds.create", "--body", "-"}, body)
	if !strings.Contains(out, `"intervalMinutes":1`) {
		t.Fatal(out)
	}
	body = strings.ReplaceAll(body, "CLI RSS", "TUI RSS")
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/auth.login"}, {"Filter: auth.login", "1"}, {"Administrator username", "rss-admin"}, {"Password (hidden)", "rss-password-strong"}, {"Enter to return:", ""}, {"Select:", "/feeds.create"}, {"Filter: feeds.create", "1"}, {"JSON body or @private-file", body}, {"Enter: more, q: stop:", ""}, {"Enter to return:", ""}, {"Select:", "q"}})
	out = invoke([]string{"call", "feeds.list"}, "")
	if !strings.Contains(out, "TUI RSS") || !strings.Contains(out, "CLI RSS") {
		t.Fatal(out)
	}
}
