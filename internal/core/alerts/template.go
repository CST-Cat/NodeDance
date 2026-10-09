package alerts

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxMessageTemplateBytes = 2048

type templatePart struct {
	literal string
	field   string
}

var notificationTemplateFields = map[string]struct{}{
	"event": {}, "alertId": {}, "ruleName": {}, "nodeName": {}, "nodeId": {},
	"subjectId": {}, "severity": {}, "message": {}, "occurredAt": {},
}

// parseMessageTemplate accepts literal text and {{fieldName}} substitutions.
// It deliberately has no filters, conditionals, or recursive expansion.
func parseMessageTemplate(template string) ([]templatePart, error) {
	if len(template) > MaxMessageTemplateBytes {
		return nil, fmt.Errorf("notification message template exceeds %d bytes", MaxMessageTemplateBytes)
	}
	parts := make([]templatePart, 0, 8)
	for offset := 0; offset < len(template); {
		open := strings.Index(template[offset:], "{{")
		close := strings.Index(template[offset:], "}}")
		if close >= 0 && (open < 0 || close < open) {
			return nil, errors.New("notification message template has an unmatched closing delimiter")
		}
		if open < 0 {
			if offset < len(template) {
				parts = append(parts, templatePart{literal: template[offset:]})
			}
			break
		}
		open += offset
		if open > offset {
			parts = append(parts, templatePart{literal: template[offset:open]})
		}
		fieldStart := open + 2
		closeRelative := strings.Index(template[fieldStart:], "}}")
		if closeRelative < 0 {
			return nil, errors.New("notification message template has an unclosed field")
		}
		close = fieldStart + closeRelative
		field := template[fieldStart:close]
		if _, ok := notificationTemplateFields[field]; !ok {
			return nil, fmt.Errorf("notification message template contains unsupported field %q", field)
		}
		parts = append(parts, templatePart{field: field})
		offset = close + 2
	}
	return parts, nil
}

func ValidateMessageTemplate(template string) error {
	_, err := parseMessageTemplate(template)
	return err
}

// RenderMessageTemplate substitutes only allowlisted Notification fields.
// Values are written once and never parsed as template source.
func RenderMessageTemplate(template string, notification Notification) (string, error) {
	if template == "" {
		return notification.Message, nil
	}
	parts, err := parseMessageTemplate(template)
	if err != nil {
		return "", err
	}
	values := map[string]string{
		"event":      notification.Event,
		"alertId":    notification.AlertID,
		"ruleName":   notification.RuleName,
		"nodeName":   notification.NodeName,
		"nodeId":     notification.NodeID,
		"subjectId":  notification.SubjectID,
		"severity":   notification.Severity,
		"message":    notification.Message,
		"occurredAt": notification.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	var rendered strings.Builder
	for _, part := range parts {
		if part.field == "" {
			rendered.WriteString(part.literal)
			continue
		}
		rendered.WriteString(values[part.field])
	}
	return rendered.String(), nil
}
