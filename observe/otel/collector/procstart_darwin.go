//go:build darwin

package collector

import "golang.org/x/sys/unix"

// readProcStartTime returns the process start time (microseconds since the
// epoch, from the kernel's KERN_PROC start-time field) via sysctl. Returns
// ok=false when the pid is gone or the sysctl call otherwise fails. This is
// the Darwin counterpart to procstart_linux.go's /proc/<pid>/stat read —
// bug-6882ecaa: before this file existed, readProcStartTime had no Darwin
// implementation at all, so IsCollectorAlive's start-time comparison could
// never run on macOS and silently fell back to "unverifiable → assume
// alive," defeating the PID-reuse identity guard ReapCollector exists to
// provide (TestReapCollector_IdentityGuard caught this by nearly killing
// its own test process — see that test's doc comment).
func readProcStartTime(pid int) (uint64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, false
	}
	tv := kp.Proc.P_starttime
	if tv.Sec == 0 && tv.Usec == 0 {
		return 0, false
	}
	return uint64(tv.Sec)*1_000_000 + uint64(tv.Usec), true
}
