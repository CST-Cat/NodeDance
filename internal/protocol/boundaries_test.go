package protocol_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeDependencyBoundaries(t *testing.T) {
	for _, area := range []string{"agent", "core", "protocol"} {
		root := filepath.Join("..", area)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range parsed.Imports {
				dep, _ := strconv.Unquote(imported.Path.Value)
				crosses := area == "agent" && strings.Contains(dep, "/internal/core") || area == "core" && strings.Contains(dep, "/internal/agent") || area == "protocol" && (strings.Contains(dep, "/internal/core") || strings.Contains(dep, "/internal/agent"))
				if crosses {
					t.Errorf("%s imports %s: Core/Agent must meet only through the wire contract", path, dep)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
