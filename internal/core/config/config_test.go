package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsTrailingJSONAndInvalidHTTPOrigin(t *testing.T) {
	for _, data := range []string{`{} {"listen":"0.0.0.0:8180"}`, `{} garbage`, `{"public_origin":"http://localhost:65536"}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("accepted invalid config: %s", data)
		}
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{\"public_origin\":\"http://127.0.0.1:8180\"}\n\t"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal("valid dev origin/trailing whitespace rejected", err)
	}
}
