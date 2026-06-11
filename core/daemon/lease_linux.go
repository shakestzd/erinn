//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// leaseTestFallbackEnv, when set to a non-empty value, enables a relaxed
// project-root-name fallback in isWriterProcessImpl for hermetic tests. It is
// NEVER set in production — see the finding-7 note below.
const leaseTestFallbackEnv = "WIPNOTE_LEASE_TEST_FALLBACK"

// isWriterProcessImpl verifies via /proc/<pid>/cmdline that the given PID is
// running a wipnote process (the most basic guard against PID reuse).
//
// The verification strategy:
//  1. Read /proc/<pid>/cmdline (NUL-separated argv).
//  2. Confirm that the args contain "wipnote" (the binary name).
//
// This is a best-effort guard against PID reuse: if a user starts an
// unrelated process after a wipnote writer crashes, that process is very
// unlikely to have "wipnote" in its cmdline. If the process exited, the /proc
// entry is gone and we return false (dead process = not holding lease).
//
// TEST-ONLY FALLBACK (bug-fddf5820, finding 7): the previous implementation
// also returned true whenever the project root's base name contained
// "wipnote". In production this is unsafe — wipnote frequently lives at a path
// like /workspaces/wipnote, so the fallback matched EVERY live PID, defeating
// the PID-reuse guard entirely (any unrelated process inheriting a recycled PID
// would be treated as the live writer). The fallback now fires ONLY when the
// WIPNOTE_LEASE_TEST_FALLBACK env var is set, which tests do explicitly. The
// process cwd/exe cannot be cheaply made reliable for the go test binary
// (compiled to a temp path without "wipnote"), so an opt-in env guard is the
// safe choice: production behaviour is strict, tests opt in.
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
	// Test-only fallback: gated behind an env var so it never weakens the
	// production PID-reuse guard. See the function comment above.
	if os.Getenv(leaseTestFallbackEnv) != "" && leasePath != "" {
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
