// Package alerts owns durable alert rules, lifecycle state and notification
// delivery records. Evaluation inputs are supplied by Core using trusted,
// current node/metric/Docker/probe state.
package alerts

import (
	"errors"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KindNodeOffline       = "node_offline"
	KindCPU               = "cpu"
	KindMemory            = "memory"
	KindDisk              = "disk"
	KindDockerUnavailable = "docker_unavailable"
	KindContainerState    = "container_state"
	KindProbeState        = "probe_state"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	ChannelWebhook = "webhook"
	ChannelSMTP    = "smtp"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Rule struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	NodeID          string   `json:"nodeId"`
	SubjectID       string   `json:"subjectId,omitempty"`
	Severity        string   `json:"severity"`
	Threshold       *float64 `json:"threshold,omitempty"`
	DurationSeconds int      `json:"durationSeconds"`
	CooldownSeconds int      `json:"cooldownSeconds"`
	ExpectedState   string   `json:"expectedState,omitempty"`
	ChannelIDs      []string `json:"channelIds"`
	Enabled         bool     `json:"enabled"`
	Revision        int64    `json:"revision"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

type RuleInput struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	NodeID          string   `json:"nodeId"`
	SubjectID       string   `json:"subjectId,omitempty"`
	Severity        string   `json:"severity"`
	Threshold       *float64 `json:"threshold,omitempty"`
	DurationSeconds int      `json:"durationSeconds"`
	CooldownSeconds int      `json:"cooldownSeconds"`
	ExpectedState   string   `json:"expectedState,omitempty"`
	ChannelIDs      []string `json:"channelIds"`
	Enabled         bool     `json:"enabled"`
	Revision        int64    `json:"revision,omitempty"`
}

type Sample struct {
	NodeID     string
	NodeName   string
	Kind       string
	SubjectID  string
	Known      bool
	Value      *float64
	State      string
	Health     string
	Message    string
	ObservedAt time.Time
}

type Alert struct {
	ID                string     `json:"id"`
	RuleID            string     `json:"ruleId"`
	RuleName          string     `json:"ruleName"`
	NodeID            string     `json:"nodeId"`
	NodeName          string     `json:"nodeName"`
	SubjectID         string     `json:"subjectId,omitempty"`
	Severity          string     `json:"severity"`
	Status            string     `json:"status"`
	Message           string     `json:"message"`
	CurrentValue      *float64   `json:"currentValue,omitempty"`
	FirstSeenAt       time.Time  `json:"firstSeenAt"`
	LastSeenAt        time.Time  `json:"lastSeenAt"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	AcknowledgedAt    *time.Time `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy    string     `json:"acknowledgedBy,omitempty"`
	AcknowledgedNote  string     `json:"acknowledgedNote,omitempty"`
	SilencedUntil     *time.Time `json:"silencedUntil,omitempty"`
	LastNotifiedAt    *time.Time `json:"lastNotifiedAt,omitempty"`
	SuppressionReason string     `json:"suppressionReason,omitempty"`
}

type Event struct {
	ID         string         `json:"id"`
	AlertID    string         `json:"alertId"`
	Kind       string         `json:"kind"`
	OccurredAt time.Time      `json:"occurredAt"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
}

type ChannelConfig struct {
	WebhookURL   string `json:"webhookUrl,omitempty"`
	SMTPHost     string `json:"smtpHost,omitempty"`
	SMTPPort     int    `json:"smtpPort,omitempty"`
	SMTPFrom     string `json:"smtpFrom,omitempty"`
	SMTPTo       string `json:"smtpTo,omitempty"`
	SMTPUsername string `json:"smtpUsername,omitempty"`
}

type Channel struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	Config    ChannelConfig `json:"config"`
	Enabled   bool          `json:"enabled"`
	HasSecret bool          `json:"hasSecret"`
	Revision  int64         `json:"revision"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt string        `json:"updatedAt"`
}

type ChannelInput struct {
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Config   ChannelConfig `json:"config"`
	Secret   string        `json:"secret,omitempty"`
	Enabled  bool          `json:"enabled"`
	Revision int64         `json:"revision,omitempty"`
}

type Window struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	ScopeType string    `json:"scopeType"`
	ScopeID   string    `json:"scopeId,omitempty"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

type WindowInput struct {
	Kind      string    `json:"kind"`
	ScopeType string    `json:"scopeType"`
	ScopeID   string    `json:"scopeId,omitempty"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Reason    string    `json:"reason"`
}

type Delivery struct {
	ID          string     `json:"id"`
	AlertID     string     `json:"alertId,omitempty"`
	ChannelID   string     `json:"channelId"`
	ChannelName string     `json:"channelName"`
	Kind        string     `json:"kind"`
	TestSend    bool       `json:"testSend"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"maxAttempts"`
	NextAttempt *time.Time `json:"nextAttemptAt,omitempty"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	HTTPStatus  int        `json:"httpStatus,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	PayloadJSON string     `json:"-"`
}

type Notification struct {
	AlertID    string    `json:"alertId,omitempty"`
	RuleID     string    `json:"ruleId,omitempty"`
	Event      string    `json:"event"`
	RuleName   string    `json:"ruleName,omitempty"`
	NodeID     string    `json:"nodeId,omitempty"`
	NodeName   string    `json:"nodeName,omitempty"`
	SubjectID  string    `json:"subjectId,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

func ValidateRule(input RuleInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("rule name must contain 1 to 80 printable characters")
	}
	if !uuidPattern.MatchString(input.NodeID) {
		return errors.New("alert rule node ID must be a canonical UUID")
	}
	if input.Severity != SeverityInfo && input.Severity != SeverityWarning && input.Severity != SeverityCritical {
		return errors.New("unsupported alert severity")
	}
	if input.DurationSeconds < 0 || input.DurationSeconds > 86400 || input.CooldownSeconds < 0 || input.CooldownSeconds > 86400 {
		return errors.New("alert duration and cooldown must be between 0 and 86400 seconds")
	}
	switch input.Kind {
	case KindNodeOffline, KindDockerUnavailable:
		if input.SubjectID != "" || input.Threshold != nil || input.ExpectedState != "" {
			return errors.New("node-level alert rule has incompatible condition fields")
		}
	case KindCPU, KindMemory, KindDisk:
		if input.Threshold == nil || math.IsNaN(*input.Threshold) || math.IsInf(*input.Threshold, 0) || *input.Threshold < 0 || *input.Threshold > 100 || input.ExpectedState != "" {
			return errors.New("resource alert threshold must be between 0 and 100 percent")
		}
		if input.Kind != KindDisk && input.SubjectID != "" {
			return errors.New("only disk rules can select a subject")
		}
	case KindContainerState:
		if input.SubjectID == "" || input.Threshold != nil || (input.ExpectedState != "running" && input.ExpectedState != "healthy" && input.ExpectedState != "stopped" && input.ExpectedState != "unhealthy") {
			return errors.New("container rule requires a container ID and supported expected state")
		}
	case KindProbeState:
		if !uuidPattern.MatchString(input.SubjectID) || input.Threshold != nil || (input.ExpectedState != "healthy" && input.ExpectedState != "unhealthy") {
			return errors.New("probe rule requires a probe UUID and expected healthy/unhealthy state")
		}
	default:
		return errors.New("unsupported alert rule kind")
	}
	if input.Revision < 0 {
		return errors.New("alert rule revision cannot be negative")
	}
	if len(input.ChannelIDs) > 16 {
		return errors.New("alert rule may target at most 16 channels")
	}
	for _, id := range input.ChannelIDs {
		if !uuidPattern.MatchString(id) {
			return errors.New("notification channel ID must be a canonical UUID")
		}
	}
	return nil
}

func ValidateChannel(input ChannelInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("channel name must contain 1 to 80 printable characters")
	}
	switch input.Kind {
	case ChannelWebhook:
		parsed, err := url.ParseRequestURI(strings.TrimSpace(input.Config.WebhookURL))
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return errors.New("webhook URL must be an HTTP(S) URL without embedded credentials")
		}
		if input.Config.SMTPHost != "" || input.Config.SMTPPort != 0 || input.Config.SMTPFrom != "" || input.Config.SMTPTo != "" || input.Config.SMTPUsername != "" {
			return errors.New("webhook channel cannot contain SMTP settings")
		}
	case ChannelSMTP:
		if strings.TrimSpace(input.Config.SMTPHost) == "" || len(input.Config.SMTPHost) > 253 || input.Config.SMTPPort < 1 || input.Config.SMTPPort > 65535 {
			return errors.New("SMTP host and port are required")
		}
		if _, err := mail.ParseAddress(input.Config.SMTPFrom); err != nil {
			return errors.New("SMTP sender address is invalid")
		}
		if _, err := mail.ParseAddress(input.Config.SMTPTo); err != nil {
			return errors.New("SMTP recipient address is invalid")
		}
		if strings.ContainsAny(input.Config.SMTPUsername, "\r\n\x00") || len(input.Config.SMTPUsername) > 256 {
			return errors.New("SMTP username is invalid")
		}
		if input.Config.WebhookURL != "" {
			return errors.New("SMTP channel cannot contain webhook settings")
		}
	default:
		return errors.New("unsupported notification channel kind")
	}
	if len(input.Secret) > 4096 || strings.ContainsRune(input.Secret, '\x00') {
		return errors.New("channel secret exceeds its limit or contains invalid data")
	}
	return nil
}
