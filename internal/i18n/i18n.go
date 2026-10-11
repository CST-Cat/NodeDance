// Package i18n localizes presentation text without changing wire identifiers or data.
package i18n

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

//go:embed zh-CN.json
var chineseJSON []byte
var chinese = func() map[string]string {
	var m map[string]string
	if err := json.Unmarshal(chineseJSON, &m); err != nil {
		panic(err)
	}
	return m
}()

type Locale string

const (
	English           Locale = "en"
	SimplifiedChinese Locale = "zh-CN"
)

type contextKey struct{}

func WithLocale(ctx context.Context, l Locale) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}
func From(ctx context.Context) Locale {
	l, _ := ctx.Value(contextKey{}).(Locale)
	if l == "" {
		return English
	}
	return l
}
func (l Locale) Text(s string) string {
	if l == SimplifiedChinese {
		if v, ok := chinese[s]; ok {
			return v
		}
	}
	return s
}
func Parse(s string) (Locale, error) {
	switch s {
	case "en":
		return English, nil
	case "zh-CN":
		return SimplifiedChinese, nil
	default:
		return "", fmt.Errorf("unsupported language %q; use en or zh-CN / 不支持的语言，请使用 en 或 zh-CN", s)
	}
}

// Options handles the global option before command-specific flags. Everything
// after -- is application data and must be left untouched.
func Options(ctx context.Context, args []string, lookup func(string) (string, bool)) (context.Context, []string, error) {
	l := From(ctx)
	cliLanguage := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		v := ""
		if a == "--lang" {
			i++
			if i == len(args) {
				return ctx, nil, fmt.Errorf("--lang requires en or zh-CN / --lang 需要 en 或 zh-CN")
			}
			v = args[i]
		} else if strings.HasPrefix(a, "--lang=") {
			v = strings.TrimPrefix(a, "--lang=")
		} else {
			rest = append(rest, a)
			continue
		}
		var err error
		l, err = Parse(v)
		cliLanguage = true
		if err != nil {
			return ctx, nil, err
		}
	}
	if !cliLanguage {
		for _, key := range []string{"NODEDANCE_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
			if ctx.Value(contextKey{}) != nil {
				break
			}
			if v, ok := lookup(key); ok && v != "" {
				if key == "NODEDANCE_LANG" {
					var err error
					l, err = Parse(v)
					if err != nil {
						return ctx, nil, err
					}
				} else if strings.HasPrefix(strings.ToLower(v), "zh") {
					l = SimplifiedChinese
				} else {
					l = English
				}
				break
			}
		}
	}
	return WithLocale(ctx, l), rest, nil
}

type writer struct {
	io.Writer
	language Locale
}

func Writer(w io.Writer, l Locale) io.Writer { return writer{w, l} }
func TextFor(w io.Writer, s string) string {
	if v, ok := w.(writer); ok {
		return v.language.Text(s)
	}
	return s
}

// LocalizedError keeps the original cause available to errors.Is/As.
type localizedError struct {
	cause error
	text  string
}

func (e localizedError) Error() string { return e.text }
func (e localizedError) Unwrap() error { return e.cause }
func (l Locale) Error(err error) error {
	if err == nil || l == English {
		return err
	}
	text := l.ErrorText(err.Error())
	if text == err.Error() {
		return err
	}
	return localizedError{err, text}
}
