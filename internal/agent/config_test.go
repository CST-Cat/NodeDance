package agent

import "testing"

func TestParseServerURLSupportsSchemeSelectedTransport(t *testing.T) {
	tests := []struct {
		name        string
		server      string
		development bool
		want        string
		wantErr     bool
	}{
		{name: "public HTTPS", server: "https://core.example", want: "https://core.example"},
		{name: "private network HTTP", server: "http://100.111.176.125:8180/", want: "http://100.111.176.125:8180"},
		{name: "development loopback HTTP", server: "http://127.0.0.1:8180", development: true, want: "http://127.0.0.1:8180"},
		{name: "development rejects remote HTTP", server: "http://100.111.176.125:8180", development: true, wantErr: true},
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
