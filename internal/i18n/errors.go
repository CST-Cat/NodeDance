package i18n

import (
	"regexp"
	"sort"
	"strings"
)

type errorTemplate struct {
	pattern    *regexp.Regexp
	translated []string
}

var errorTemplates = func() []errorTemplate {
	keys := make([]string, 0, len(chinese))
	for key, value := range chinese {
		if strings.Contains(key, "%") && key != value {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	slot := regexp.MustCompile(`%[sqvdw]`)
	result := []errorTemplate{}
	for _, key := range keys {
		pieces := slot.Split(key, -1)
		translated := slot.Split(chinese[key], -1)
		if len(pieces) < 2 || len(pieces) != len(translated) {
			continue
		}
		pattern := "^" + regexp.QuoteMeta(pieces[0])
		for _, p := range pieces[1:] {
			pattern += "([\\s\\S]*?)" + regexp.QuoteMeta(p)
		}
		pattern += "$"
		result = append(result, errorTemplate{regexp.MustCompile(pattern), translated})
	}
	return result
}()

func (l Locale) ErrorText(text string) string {
	if l == English {
		return text
	}
	if value, ok := chinese[text]; ok {
		return value
	}
	for _, template := range errorTemplates {
		if m := template.pattern.FindStringSubmatch(text); m != nil {
			var out strings.Builder
			out.WriteString(template.translated[0])
			for i, capture := range m[1:] {
				out.WriteString(capture)
				out.WriteString(template.translated[i+1])
			}
			return out.String()
		}
	}
	return text
}
