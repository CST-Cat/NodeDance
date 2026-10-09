package alerts

import (
	"context"
	"strings"
	"testing"
)

func TestSMTPRequiresExplicitTLSMode(t *testing.T) {
	base := ChannelInput{
		Name: "Local SMTP", Kind: ChannelSMTP,
		Config: ChannelConfig{
			SMTPHost: "127.0.0.1", SMTPPort: 2525,
			SMTPFrom: "alerts@example.test", SMTPTo: "ops@example.test",
		},
	}
	for _, mode := range []string{"", SMTPSecuritySTARTTLS, SMTPSecurityImplicitTLS} {
		t.Run("validate_"+mode, func(t *testing.T) {
			input := base
			input.Config.SMTPSecurityMode = mode
			err := ValidateChannel(input)
			if mode == "" {
				if err == nil || !strings.Contains(err.Error(), "security mode") {
					t.Fatalf("empty SMTP security mode must fail validation, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("explicit TLS mode %q must validate: %v", mode, err)
			}
		})
	}

	err := NewSender().sendSMTP(context.Background(), base.Config, "", "test message")
	if err == nil || err.Error() != "unsupported SMTP security mode" {
		t.Fatalf("stored empty mode must fail closed before connecting, got %v", err)
	}
}
