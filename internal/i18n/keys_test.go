package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// sourceCorpus concatenates every string literal in non-test Go sources, so a
// translation key must be reachable from product code to stay in the table.
// Keys built at runtime must therefore appear as complete fmt format strings.
func sourceCorpus(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the i18n package on disk")
	}
	root := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	var builder strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "web", ".build", ".tools", ".git", ".zcode", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			builder.WriteString(value)
			builder.WriteByte(0)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return builder.String()
}

func TestChineseKeysAreReachableFromProductCode(t *testing.T) {
	corpus := sourceCorpus(t)
	for key := range chinese {
		if !strings.Contains(corpus, key) {
			t.Errorf("zh-CN key has no product source reference: %q", key)
		}
	}
}
