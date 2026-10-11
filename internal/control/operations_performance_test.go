package control

import (
	"reflect"
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
