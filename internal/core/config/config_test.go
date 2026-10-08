package config

import "testing"

func TestResolveListenPrecedence(t *testing.T) {
	tests := []struct {
		name string
		src  Sources
		want string
	}{
		{name: "default", want: DefaultListen},
		{name: "config", src: Sources{Config: File{Listen: "127.0.0.1:9001"}}, want: "127.0.0.1:9001"},
		{name: "environment beats config", src: Sources{EnvListen: "127.0.0.1:9002", Config: File{Listen: "127.0.0.1:9001"}}, want: "127.0.0.1:9002"},
		{name: "CLI beats environment and config", src: Sources{CLIListen: "127.0.0.1:9003", EnvListen: "127.0.0.1:9002", Config: File{Listen: "127.0.0.1:9001"}}, want: "127.0.0.1:9003"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveListen(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("ResolveListen() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateDevListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8180", "[::1]:8180"} {
		if err := ValidateDevListen(address); err != nil {
			t.Errorf("ValidateDevListen(%q): %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8180", "192.0.2.1:8180", "localhost:8180"} {
		if err := ValidateDevListen(address); err == nil {
			t.Errorf("ValidateDevListen(%q) unexpectedly succeeded", address)
		}
	}
}

func TestValidateListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8180", "192.0.2.15:18180", "[::1]:8180"} {
		if err := ValidateListen(address); err != nil {
			t.Errorf("ValidateListen(%q): %v", address, err)
		}
	}
	for _, address := range []string{"localhost:8180", "0.0.0.0", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:not-a-port", "127.0.0.1:8180junk", "127.0.0.1:+8180"} {
		if err := ValidateListen(address); err == nil {
			t.Errorf("ValidateListen(%q) unexpectedly succeeded", address)
		}
	}
}
