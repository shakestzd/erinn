//go:build linux

package daemon

import (
	"os"
	"strconv"
	"strings"
)

// isWriterProcessImpl verifies via /proc/<pid>/cmdline that the given PID is
// running a wipnote process (the most basic guard against PID reuse).
//
// The verification strategy:
//  1. Read /proc/<pid>/cmdline (NUL-separated argv).
//  2. Confirm that the args contain "wipnote" (the binary).
//
// This is a best-effort guard against PID reuse: if a user starts an
// unrelated process after a wipnote writer crashes, that process is very
// unlikely to have "wipnote" in its cmdline. If the process exited, the
// /proc entry is gone and we return false (dead process = not holding lease).
//
// leasePath is accepted for future extensions (e.g., project-dir matching)
// but not currently used.
func isWriterProcessImpl(pid int, leasePath string) bool {
	cmdlinePath := "/proc/" + strconv.Itoa(pid) + "/cmdline"
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		// /proc entry gone — process already exited.
		return false
	}
	// cmdline is NUL-separated; replace NULs with spaces for substring search.
	args := strings.ReplaceAll(string(data), "\x00", " ")
	// Check if this is a wipnote process. This is a best-effort guard against
	// PID reuse — very unlikely an unrelated process has "wipnote" in its name.
	return strings.Contains(args, "wipnote")
}
