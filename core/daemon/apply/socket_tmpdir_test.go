package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps this package's Unix-domain socket paths within the platform's
// sockaddr_un.sun_path limit. These tests stand up a real writer daemon bound at
// <projectRoot>/.wipnote/writer.sock with projectRoot = t.TempDir(); on macOS the
// default per-user temp base (/var/folders/<hash>/T) makes that path exceed the
// 104-byte sun_path cap, so bind() fails with EINVAL ("bind: invalid argument").
// See core/daemon/socket_tmpdir_test.go for the full rationale — this mirrors it
// because each package builds its own test binary and Go runs one TestMain per
// test binary.
func TestMain(m *testing.M) {
	ensureSocketSafeTmpdir()
	os.Exit(m.Run())
}

func ensureSocketSafeTmpdir() {
	const sunPathCap = 104 // darwin/BSD = 104; Linux = 108.
	probe := filepath.Join(os.TempDir(), "T"+strings.Repeat("x", 70), "001", ".wipnote", "writer.sock")
	if len(probe) < sunPathCap {
		return // current base already fits — no-op on Linux/devcontainer.
	}
	if _, err := os.Stat("/tmp"); err == nil {
		_ = os.Setenv("TMPDIR", "/tmp")
	}
}
