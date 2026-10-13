package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSaveConfigRequiresExplicitCurrentSchema(t *testing.T) {
	for _, schema := range []int{0, ConfigSchema, ConfigSchema + 1} {
		t.Run(strconv.Itoa(schema), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "agent.json")
			config := Config{Schema: schema, Server: "https://core.example", Credential: strings.Repeat("a", 64)}
			err := SaveConfig(path, config, true)
			if schema != ConfigSchema {
				if err == nil {
					t.Fatal("saved configuration without the current schema")
				}
				if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("invalid configuration created state directory: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if err != nil || loaded.Schema != ConfigSchema || loaded.Credential != config.Credential {
				t.Fatalf("current configuration did not round trip: %+v, %v", loaded, err)
			}
		})
	}
}

func TestParseServerURLRequiresHTTPSOutsideLoopbackDevelopment(t *testing.T) {
	tests := []struct {
		name        string
		server      string
		development bool
		want        string
		wantErr     bool
	}{
		{name: "public HTTPS", server: "https://core.example", want: "https://core.example"},
		{name: "private network HTTP rejected", server: "http://100.111.176.125:8180", wantErr: true},
		{name: "loopback HTTP rejected outside development", server: "http://127.0.0.1:8180", wantErr: true},
		{name: "development loopback HTTP", server: "http://127.0.0.1:8180", development: true, want: "http://127.0.0.1:8180"},
		{name: "development rejects private network HTTP", server: "http://100.111.176.125:8180", development: true, wantErr: true},
		{name: "rejects URL path", server: "https://core.example/path", wantErr: true},
		{name: "rejects unsupported scheme", server: "ftp://core.example", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseServerURL(test.server, test.development)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseServerURL(%q) succeeded; want error", test.server)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseServerURL(%q): %v", test.server, err)
			}
			if got != test.want {
				t.Fatalf("ParseServerURL(%q) = %q, want %q", test.server, got, test.want)
			}
		})
	}
}
