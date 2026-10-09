package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderMessageTemplateUsesAllowlistedFieldsOnce(t *testing.T) {
	notification := Notification{
		Event: "firing", AlertID: "alert-123", RuleName: "CPU high", NodeName: "node-one",
		NodeID: "node-123", SubjectID: "container-456", Severity: SeverityCritical,
		Message:    "<script>alert(1)</script> {{ruleName}}\r\nSubject: forged",
		OccurredAt: time.Date(2026, 10, 9, 7, 8, 9, 123000000, time.FixedZone("PDT", -7*60*60)),
	}
	template := "{{event}}|{{alertId}}|{{ruleName}}|{{nodeName}}|{{nodeId}}|{{subjectId}}|{{severity}}|{{message}}|{{occurredAt}}"
	got, err := RenderMessageTemplate(template, notification)
	if err != nil {
		t.Fatal(err)
	}
	want := "firing|alert-123|CPU high|node-one|node-123|container-456|critical|<script>alert(1)</script> {{ruleName}}\r\nSubject: forged|2026-10-09T14:08:09.123Z"
	if got != want {
		t.Fatalf("rendered template = %q, want %q", got, want)
	}

	encoded, err := json.Marshal(Notification{Message: got})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Notification
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Message != want {
		t.Fatalf("JSON notification did not preserve rendered value: %q err=%v", decoded.Message, err)
	}
	if strings.Contains(string(encoded), "\r\nSubject: forged") {
		t.Fatal("JSON webhook bytes contain an unescaped injected line break")
	}

	mailBody, err := smtpNotificationText(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mailBody, "\r\nSubject: forged") || !strings.Contains(mailBody, `\r\nSubject: forged`) {
		t.Fatalf("SMTP body did not safely escape a header-looking value: %q", mailBody)
	}
}

func TestMessageTemplateRejectsUnknownMalformedAndOversizedInput(t *testing.T) {
	for _, template := range []string{
		"{{hostname}}",
		"{{message | upper}}",
		"prefix {{message",
		"message}} suffix",
		strings.Repeat("x", MaxMessageTemplateBytes+1),
	} {
		if err := ValidateMessageTemplate(template); err == nil {
			t.Errorf("ValidateMessageTemplate(%q) succeeded, want rejection", template)
		}
	}
	if err := ValidateMessageTemplate(""); err != nil {
		t.Fatalf("empty legacy template rejected: %v", err)
	}
	legacyMessage := "NodeDance notification channel test"
	got, err := RenderMessageTemplate("", Notification{Message: legacyMessage})
	if err != nil || got != legacyMessage {
		t.Fatalf("empty template changed legacy message: %q err=%v", got, err)
	}
}
