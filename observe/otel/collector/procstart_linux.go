//go:build linux

package collector

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readProcStartTime returns the process start time from /proc/<pid>/stat
// field 22 (clock ticks since boot). Returns ok=false when the proc entry
// is unreadable (process gone, permission denied, etc). Used for PID-reuse
// detection by IsCollectorAlive.
func readProcStartTime(pid int) (uint64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	// Field 2 (comm) is wrapped in parens and may itself contain spaces or
	// parens. Split after the LAST closing paren.
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+1 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[idx+1:])
	// Index 0 here corresponds to field 3 (state); field 22 (starttime) is
	// at index 19 of this slice.
	if len(fields) < 20 {
		return 0, false
	}
	st, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return st, true
}
