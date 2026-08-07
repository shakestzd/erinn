package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the daemon test suite's Unix-domain socket paths within the
// platform's sockaddr_un.sun_path limit.
//
// The writer daemon binds a per-project socket at
// <projectRoot>/.wipnote/writer.sock, and the tests use t.TempDir() as the
// project root. Go's t.TempDir() embeds the (long) test name under
// os.TempDir(); on macOS the default per-user base (/var/folders/<hash>/T,
// already ~50 bytes) pushes the full socket path past sun_path's 104-byte cap,
// so bind() fails with EINVAL, surfaced by the test runner as
// "bind: invalid argument". Linux's sun_path cap is 108 and its default temp
// base is the short "/tmp", so those paths already fit.
//
// ensureSocketSafeTmpdir points TMPDIR at "/tmp" (the shortest writable base) ONLY
// when the current base would overflow the cap — a no-op on Linux/devcontainer,
// so it never regresses that path. t.TempDir() still auto-cleans its dirs.
func TestMain(m *testing.M) {
	ensureSocketSafeTmpdir()
	os.Exit(m.Run())
}

func ensureSocketSafeTmpdir() {
	// Cap is the smallest common sun_path limit (darwin/BSD = 104; Linux = 108).
	const sunPathCap = 104
	// Probe the longest socket path a test here could bind under the current base:
	// a generously long test-name component plus the fixed socket suffix.
	probe := filepath.Join(os.TempDir(), "T"+strings.Repeat("x", 70), "001", ".wipnote", "writer.sock")
	if len(probe) < sunPathCap {
		return // current base already fits — leave TMPDIR untouched (Linux path).
	}
	if _, err := os.Stat("/tmp"); err == nil {
		_ = os.Setenv("TMPDIR", "/tmp")
	}
}
