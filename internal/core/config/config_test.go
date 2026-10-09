package config

import (
	"path/filepath"
	"testing"
	"time"
)

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

func TestDataDirPrecedenceAndXDGDefaults(t *testing.T) {
	if got, want := ResolveDataDir(Sources{XDGDataHome: "/home/test/.local/share", UserHome: "/home/test"}), "/home/test/.local/share/nodedance"; got != want {
		t.Fatalf("XDG data directory=%q, want %q", got, want)
	}
	if got, want := ResolveDataDir(Sources{UserHome: "/home/test"}), "/home/test/.local/share/nodedance"; got != want {
		t.Fatalf("home data directory=%q, want %q", got, want)
	}
	tests := []struct {
		name string
		src  Sources
		want string
	}{
		{name: "config", src: Sources{Config: File{DataDir: "./config-data"}, UserHome: "/home/test"}, want: "config-data"},
		{name: "environment overrides config", src: Sources{EnvDataDir: "./environment-data", Config: File{DataDir: "./config-data"}}, want: "environment-data"},
		{name: "CLI overrides environment and config", src: Sources{CLIDataDir: "./cli-data", EnvDataDir: "./environment-data", Config: File{DataDir: "./config-data"}}, want: "cli-data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveDataDir(tt.src); got != filepath.Clean(tt.want) {
				t.Fatalf("ResolveDataDir()=%q, want %q", got, filepath.Clean(tt.want))
			}
		})
	}
}

func TestPublicOriginAndTrustedProxyValidation(t *testing.T) {
	if err := ValidatePublicOrigin("https://panel.example.test", false); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePublicOrigin("http://127.0.0.1:8180", false); err == nil {
		t.Fatal("production accepted an HTTP public origin")
	}
	if err := ValidatePublicOrigin("http://127.0.0.1:8180", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies([]string{"127.0.0.1", "10.20.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies([]string{"not-a-proxy"}); err == nil {
		t.Fatal("invalid trusted proxy was accepted")
	}
}

func TestTimingOverridesAreDevelopmentOnly(t *testing.T) {
	file := File{SessionIdleTimeout: "2s", LoginMaxAttempts: 3, LoginLockoutDuration: "5s"}
	idle, attempts, cooldown, err := RuntimeValues(file, true)
	if err != nil {
		t.Fatal(err)
	}
	if idle != 2*time.Second || attempts != 3 || cooldown != 5*time.Second {
		t.Fatalf("development timing values=(%s,%d,%s)", idle, attempts, cooldown)
	}
	if _, _, _, err := RuntimeValues(file, false); err == nil {
		t.Fatal("production accepted test timing overrides")
	}
	if idle, attempts, cooldown, err = RuntimeValues(File{}, false); err != nil {
		t.Fatal(err)
	}
	if idle != DefaultSessionIdleTimeout || attempts != DefaultLoginMaxAttempts || cooldown != DefaultLoginLockoutDuration {
		t.Fatalf("production defaults=(%s,%d,%s)", idle, attempts, cooldown)
	}
}

func TestWebSocketCheckIntervalIsBoundedAndTestOnly(t *testing.T) {
	if got, err := RuntimeWebSocketCheckInterval(File{}, false); err != nil || got != 10*time.Second {
		t.Fatalf("production default interval=%s err=%v", got, err)
	}
	if got, err := RuntimeWebSocketCheckInterval(File{WebSocketCheckInterval: "50ms"}, true); err != nil || got != 50*time.Millisecond {
		t.Fatalf("development interval=%s err=%v", got, err)
	}
	if _, err := RuntimeWebSocketCheckInterval(File{WebSocketCheckInterval: "50ms"}, false); err == nil {
		t.Fatal("production accepted a WebSocket test interval")
	}
	if _, err := RuntimeWebSocketCheckInterval(File{WebSocketCheckInterval: "31s"}, true); err == nil {
		t.Fatal("interval over the 30 second close bound was accepted")
	}
}

func TestRuntimeFileTransferLimitPrecedenceAndHardBound(t *testing.T) {
	for _, test := range []struct {
		name string
		file File
		env  string
		cli  string
		want int64
	}{
		{name: "default", want: 1 << 30},
		{name: "config", file: File{MaxFileTransferBytes: 2 << 30}, want: 2 << 30},
		{name: "environment overrides config", file: File{MaxFileTransferBytes: 2 << 30}, env: "3", want: 3},
		{name: "CLI overrides environment", env: "3", cli: "4", want: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := RuntimeFileTransferLimit(test.file, test.env, test.cli)
			if err != nil || got != test.want {
				t.Fatalf("limit=%d err=%v, want %d", got, err, test.want)
			}
		})
	}
	for _, value := range []string{"0", "-1", "not-a-number", "17179869185"} {
		if _, err := RuntimeFileTransferLimit(File{}, value, ""); err == nil {
			t.Errorf("invalid transfer limit %q was accepted", value)
		}
	}
}

func TestRuntimeNonMetricHistoryRetentionPrecedenceAndBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		file File
		env  string
		cli  string
		want int
	}{
		{name: "default", want: DefaultNonMetricHistoryRetentionDays},
		{name: "config", file: File{NonMetricHistoryRetentionDays: 30}, want: 30},
		{name: "environment overrides config", file: File{NonMetricHistoryRetentionDays: 30}, env: "60", want: 60},
		{name: "CLI overrides environment", env: "60", cli: "120", want: 120},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := RuntimeNonMetricHistoryRetentionDays(test.file, test.env, test.cli)
			if err != nil || got != test.want {
				t.Fatalf("retention days=%d err=%v, want %d", got, err, test.want)
			}
		})
	}
	for _, value := range []string{"0", "-1", "3651", "nope"} {
		if _, err := RuntimeNonMetricHistoryRetentionDays(File{}, value, ""); err == nil {
			t.Errorf("invalid environment retention %q was accepted", value)
		}
	}
	for _, value := range []int{0, -1, 3651} {
		if value == 0 { // zero means the documented default in JSON config.
			continue
		}
		if _, err := RuntimeNonMetricHistoryRetentionDays(File{NonMetricHistoryRetentionDays: value}, "", ""); err == nil {
			t.Errorf("invalid config retention %d was accepted", value)
		}
	}
}
