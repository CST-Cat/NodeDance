package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"unicode/utf8"
)

// ValidateJSON enforces the published structural contract. Stateful permission,
// confirmation, revision and resource checks remain in Core/Agent handlers.
func ValidateJSON(schema any, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		if s, ok := schema.(map[string]any); ok {
			required, _ := s["required"].([]string)
			if len(required) > 0 {
				return fmt.Errorf("body: missing property %s", required[0])
			}
			if keys, ok := s["required"].([]any); ok && len(keys) > 0 {
				return fmt.Errorf("body: missing property %v", keys[0])
			}
		}
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("body must contain one JSON value")
	}
	return validateValue(schema, value, "body")
}

func validateValue(description, value any, path string) error {
	s, ok := description.(map[string]any)
	if !ok {
		return nil
	}
	if value == nil && s["nullable"] == true {
		return nil
	}
	fail := func(rule string) error { return fmt.Errorf("%s: %s", path, rule) }
	if entries, ok := s["enum"]; ok {
		allowed := false
		switch values := entries.(type) {
		case []string:
			for _, v := range values {
				if value == v {
					allowed = true
				}
			}
		case []any:
			for _, v := range values {
				if reflect.DeepEqual(value, v) {
					allowed = true
				}
			}
		}
		if !allowed {
			return fail("value is not in the allowed enum")
		}
	}
	switch s["type"] {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fail("expected object")
		}
		props, _ := s["properties"].(map[string]any)
		required := []string{}
		switch keys := s["required"].(type) {
		case []string:
			required = keys
		case []any:
			for _, key := range keys {
				if k, ok := key.(string); ok {
					required = append(required, k)
				}
			}
		}
		for _, key := range required {
			if _, ok := obj[key]; !ok {
				return fail("missing property " + key)
			}
		}
		for key, v := range obj {
			if child, ok := props[key]; ok {
				if err := validateValue(child, v, path+"."+key); err != nil {
					return err
				}
			} else if s["additionalProperties"] == false {
				return fail("unknown property " + key)
			} else if child, ok := s["additionalProperties"].(map[string]any); ok {
				if err := validateValue(child, v, path+"."+key); err != nil {
					return err
				}
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fail("expected array")
		}
		if n, ok := schemaNumber(s["minItems"]); ok && float64(len(values)) < n {
			return fail("too few items")
		}
		if n, ok := schemaNumber(s["maxItems"]); ok && float64(len(values)) > n {
			return fail("too many items")
		}
		for i, v := range values {
			if err := validateValue(s["items"], v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		v, ok := value.(string)
		if !ok {
			return fail("expected string")
		}
		if n, ok := schemaNumber(s["minLength"]); ok && float64(utf8.RuneCountInString(v)) < n {
			return fail("string is too short")
		}
		if n, ok := schemaNumber(s["maxLength"]); ok && float64(utf8.RuneCountInString(v)) > n {
			return fail("string is too long")
		}
		if n, ok := schemaNumber(s["maxBytes"]); ok && float64(len(v)) > n {
			return fail("string exceeds byte limit")
		}
	case "integer", "number":
		v, ok := value.(float64)
		if !ok || (s["type"] == "integer" && math.Trunc(v) != v) {
			return fail("expected " + s["type"].(string))
		}
		if n, ok := schemaNumber(s["minimum"]); ok && v < n {
			return fail("value below minimum")
		}
		if n, ok := schemaNumber(s["maximum"]); ok && v > n {
			return fail("value above maximum")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("expected boolean")
		}
	}
	return nil
}

func schemaNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
