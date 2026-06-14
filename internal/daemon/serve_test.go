package daemon

import (
	"net"
	"testing"
	"time"
)

func TestNewServeCommandBindFlag(t *testing.T) {
	cmd := NewServeCommand(func(bind string, port int) error { return nil })

	f := cmd.Flags().Lookup("bind")
	if f == nil {
		t.Fatal("--bind flag not registered")
	}
	if f.DefValue != "127.0.0.1" {
		t.Fatalf("--bind default = %q, want 127.0.0.1", f.DefValue)
	}

	addr := "0.0.0.0:0"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("net.Listen(%q): %v", addr, err)
	}
	_ = ln.Close()
}

func TestIsValidProjectID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"a\\b", false},
		{"a\x00b", false},
		{"XYZ", false},
		{"abc", false},
		{"deadbeef", true},
		{"abcd1234", true},
		{"abc1", true},
	}
	for _, tc := range tests {
		if got := IsValidProjectID(tc.id); got != tc.want {
			t.Fatalf("IsValidProjectID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestParseProjectProxyPath(t *testing.T) {
	projectID, remainder, ok := ParseProjectProxyPath("/p/deadbeef/api/stats")
	if !ok {
		t.Fatal("expected valid proxy path")
	}
	if projectID != "deadbeef" {
		t.Fatalf("projectID = %q, want deadbeef", projectID)
	}
	if remainder != "/api/stats" {
		t.Fatalf("remainder = %q, want /api/stats", remainder)
	}
}

func TestParseProjectProxyPathRejectsInvalidID(t *testing.T) {
	for _, path := range []string{"/p/", "/p/../etc/passwd", "/p/XYZ/api/stats", "/p/ab/api/stats"} {
		if _, _, ok := ParseProjectProxyPath(path); ok {
			t.Fatalf("expected invalid proxy path: %s", path)
		}
	}
}

func TestResolveDashboardAddress(t *testing.T) {
	host, port := ResolveDashboardAddress(true, func(string) string { return "" })
	if host != "0.0.0.0" || port != 8088 {
		t.Fatalf("devcontainer defaults = %s:%d, want 0.0.0.0:8088", host, port)
	}

	host, port = ResolveDashboardAddress(false, func(key string) string {
		switch key {
		case "WIPNOTE_SERVE_BIND":
			return "127.0.0.2"
		case "WIPNOTE_SERVE_PORT":
			return "9001"
		default:
			return ""
		}
	})
	if host != "127.0.0.2" || port != 9001 {
		t.Fatalf("overrides = %s:%d, want 127.0.0.2:9001", host, port)
	}
}

func TestProbePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	if !ProbePort("127.0.0.1", addr.Port, 200*time.Millisecond) {
		t.Fatal("expected ProbePort to succeed")
	}
}
