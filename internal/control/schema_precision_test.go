package control

import "testing"

func TestExactIntegerBoundsAndNumericEnums(t *testing.T) {
	unsigned := map[string]any{"type": "integer", "format": "uint64", "maximumInteger": "18446744073709551615"}
	signed := map[string]any{"type": "integer", "format": "int64", "minimumInteger": "-9223372036854775808", "maximumInteger": "9223372036854775807"}
	for _, tc := range []struct {
		schema map[string]any
		value  string
		valid  bool
	}{
		{unsigned, "18446744073709551615", true}, {unsigned, "18446744073709551616", false}, {unsigned, "-1", false},
		{signed, "-9223372036854775808", true}, {signed, "-9223372036854775809", false}, {signed, "9223372036854775808", false},
		{map[string]any{"type": "integer", "enum": []any{1, 2}}, "2", true},
		{map[string]any{"type": "integer", "enum": []any{1, 2}}, "3", false},
	} {
		if err := ValidateJSON(tc.schema, []byte(tc.value)); (err == nil) != tc.valid {
			t.Errorf("%s valid=%v: %v", tc.value, tc.valid, err)
		}
	}
}
