package alerts

import (
	"strings"
	"testing"
)

func TestFeedSMTPUsesInertSharedNotificationTransport(t *testing.T) {
	v, e := smtpNotificationText(`{"event":"feed_digest","feedName":"RSS\r\nBcc:evil","items":[{"title":"title\n.DATA","url":"https://example.org"}]}`)
	if e != nil || strings.Contains(v, "\r\nBcc:") || strings.Contains(v, "\n.DATA") || strings.Contains(v, "Alert ID:") {
		t.Fatal(v, e)
	}
	if !strings.Contains(v, "https://example.org") {
		t.Fatal(v)
	}
}
