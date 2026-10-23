package control

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogSchemasRemainCallerOwned(t *testing.T) {
	before, err := Find("auth.setup")
	if err != nil {
		t.Fatal(err)
	}
	altered, _ := Find("auth.setup")
	schema := altered.BodySchema.(map[string]any)
	schema["required"].([]string)[0] = "corrupted"
	schema["properties"].(map[string]any)["credential"].(map[string]any)["type"] = "corrupted"
	for _, o := range Operations() {
		if o.Name == "auth.setup" {
			o.BodySchema.(map[string]any)["properties"].(map[string]any)["password"] = nil
		}
	}
	after, _ := Find("auth.setup")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("mutated shared schema")
	}
}
func BenchmarkOperationLookup(b *testing.B) {
	b.Run("build-catalog", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, o := range buildOperations() {
				if o.Name == "nodes.list" {
					break
				}
			}
		}
	})
	b.Run("indexed-copy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := Find("nodes.list"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkRouteLookup(b *testing.B) {
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/nodes/fixture/history"}, {"GET", "/api/v1/public/guest/display"}, {"POST", "/api/v1/nodes/fixture/commands"}} {
		b.Run(route.path, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, ok := MatchRoute(route.method, route.path); !ok {
					b.Fatal("route not found")
				}
			}
		})
	}
}

func TestRouteIndexPreservesContractPrecedence(t *testing.T) {
	reference := func(method, path string) (Operation, bool) {
		parts := strings.Split(path, "/")
		for _, operation := range operationCatalog() {
			if method != operation.Method {
				continue
			}
			pattern := strings.Split(operation.Path, "/")
			if len(pattern) != len(parts) {
				continue
			}
			match := true
			for i, part := range pattern {
				if strings.HasPrefix(part, "{") {
					if parts[i] == "" {
						match = false
					}
				} else if part != parts[i] {
					match = false
				}
			}
			if match {
				return operation, true
			}
		}
		return Operation{}, false
	}
	for _, operation := range operationCatalog() {
		path := operation.Path
		for _, parameter := range operation.Parameters {
			path = strings.ReplaceAll(path, "{"+parameter+"}", "fixture")
		}
		expected, exists := reference(operation.Method, path)
		actual, found := MatchRoute(operation.Method, path)
		if exists != found || expected.Name != actual.Name {
			t.Fatalf("%s %s: expected %s, got %s", operation.Method, path, expected.Name, actual.Name)
		}
	}
	for _, path := range []string{"/api/v1/nodes//history", "/api/v1/nodes/fixture/history/", "/api/v1/absent", "//api/v1/nodes"} {
		if _, found := MatchRoute("GET", path); found {
			t.Fatalf("matched malformed route %s", path)
		}
	}
	first, _ := MatchRoute("POST", "/api/v1/auth/login")
	first.BodySchema.(map[string]any)["properties"].(map[string]any)["username"] = nil
	again, _ := MatchRoute("POST", "/api/v1/auth/login")
	if again.BodySchema.(map[string]any)["properties"].(map[string]any)["username"] == nil {
		t.Fatal("route lookup shared mutable schemas")
	}
}
