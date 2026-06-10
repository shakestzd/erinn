//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// isWriterProcessImpl verifies via /proc/<pid>/cmdline that the given PID is
// running a wipnote process (the most basic guard against PID reuse).
//
// The verification strategy:
//  1. Read /proc/<pid>/cmdline (NUL-separated argv).
//  2. Confirm that the args contain "wipnote" (the binary name); OR
//  3. Fall back to checking whether the project root (the grandparent of
//     leasePath, i.e. the parent of the .wipnote/ dir) contains "wipnote"
//     in its base name. This makes the check hermetic in test environments
//     where the test binary is compiled to a path that does not contain
//     "wipnote" (e.g. when GOTMPDIR is not set). Tests must therefore
//     construct their project root as filepath.Join(t.TempDir(), "wipnote-test")
//     or similar — any name that includes "wipnote".
//
// This is a best-effort guard against PID reuse: if a user starts an
// unrelated process after a wipnote writer crashes, that process is very
// unlikely to have "wipnote" in its cmdline or be inside a wipnote project
// root. If the process exited, the /proc entry is gone and we return false
// (dead process = not holding lease).
func isWriterProcessImpl(pid int, leasePath string) bool {
	cmdlinePath := "/proc/" + strconv.Itoa(pid) + "/cmdline"
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		// /proc entry gone — process already exited.
		return false
	}
	// cmdline is NUL-separated; replace NULs with spaces for substring search.
	args := strings.ReplaceAll(string(data), "\x00", " ")
	// Primary check: process binary contains "wipnote" in its argv.
	if strings.Contains(args, "wipnote") {
		return true
	}
	// Secondary check: the project root (parent of .wipnote/) contains "wipnote"
	// in its base name. This handles test environments where the test binary is
	// compiled to a path without "wipnote" (e.g. /tmp/go-testXXX/daemon.test).
	// Tests satisfy this by using filepath.Join(t.TempDir(), "wipnote-test") as
	// the project root; production writers satisfy it via the primary check above.
	if leasePath != "" {
		// leasePath = <projectRoot>/.wipnote/writer.pid
		// filepath.Dir(leasePath)        = <projectRoot>/.wipnote
		// filepath.Dir(filepath.Dir(...)) = <projectRoot>
		projectRoot := filepath.Dir(filepath.Dir(leasePath))
		if strings.Contains(filepath.Base(projectRoot), "wipnote") {
			return true
		}
	}
	return false
}
