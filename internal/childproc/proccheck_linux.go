//go:build linux

package childproc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// isServeChildPID verifies via /proc/<pid>/cmdline that the given PID is
// running a wipnote _serve-child process associated with the project that
// owns pidFilePath.
//
// The verification strategy:
//  1. Read /proc/<pid>/cmdline (NUL-separated argv).
//  2. Confirm that the args contain both "_serve-child" and the project
//     --project-dir flag value (derived from pidFilePath).
//
// This prevents inadvertently signalling an unrelated process that reused
// a recorded PID after the original child exited.
func isServeChildPID(pid int, pidFilePath string) bool {
	cmdlinePath := "/proc/" + strconv.Itoa(pid) + "/cmdline"
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		// /proc entry gone — process already exited.
		return false
	}
	// cmdline is NUL-separated; replace NULs with spaces for substring search.
	args := strings.ReplaceAll(string(data), "\x00", " ")
	if !strings.Contains(args, "_serve-child") {
		return false
	}
	// Derive the project dir from the PID file path:
	// pidFilePath = <projectDir>/.wipnote/.serve-children.pid
	projectDir := filepath.Dir(filepath.Dir(pidFilePath))
	return strings.Contains(args, projectDir)
}
