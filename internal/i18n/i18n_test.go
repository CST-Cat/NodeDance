package i18n

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestLanguagePrecedenceAndOpaqueArguments(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key == "NODEDANCE_LANG" {
			return "zh-CN", true
		}
		return "", false
	}
	ctx, args, err := Options(context.Background(), []string{"call", "nodes.list", "--lang=en", "--", "--lang=private-data"}, lookup)
	if err != nil || From(ctx) != English || !reflect.DeepEqual(args, []string{"call", "nodes.list", "--", "--lang=private-data"}) {
		t.Fatalf("locale/args: %v %v %v", From(ctx), args, err)
	}
	ctx, _, err = Options(context.Background(), []string{"--lang=en"}, func(string) (string, bool) { return "invalid", true })
	if err != nil || From(ctx) != English {
		t.Fatal("CLI must override invalid environment", err)
	}
	for _, value := range []string{"", "fr", "zh_CN"} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if _, _, err := Options(context.Background(), []string{"--lang"}, lookup); err == nil {
		t.Fatal("accepted missing language")
	}
}
func TestMessagesPreserveFormatSlotsAndErrorCause(t *testing.T) {
	for en, zh := range chinese {
		if strings.Count(en, "%") != strings.Count(zh, "%") {
			t.Fatalf("format mismatch %q", en)
		}
	}
	original := errors.New(`unknown operation "<untrusted>"; run nodedance operations`)
	localized := SimplifiedChinese.Error(original)
	if !errors.Is(localized, original) || !strings.Contains(localized.Error(), `未知操作 "<untrusted>"`) {
		t.Fatal(localized)
	}
	if SimplifiedChinese.Text("nodes.list") != "nodes.list" {
		t.Fatal("changed identifier")
	}
	// Metadata wrappers do not translate raw output, files or terminal bytes.
	var output strings.Builder
	w := Writer(&output, SimplifiedChinese)
	io.WriteString(w, "Select\x1b[2J")
	if output.String() != "Select\x1b[2J" || TextFor(w, "Select") != "选择" {
		t.Fatal(output.String())
	}
}
