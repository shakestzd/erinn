//go:build !linux

package childproc

import "syscall"

// childSysProcAttr returns the platform-specific SysProcAttr for child
// processes. On non-Linux platforms Pdeathsig is unavailable; we still
// set Setpgid so the child is in its own process group and a SIGKILL to
// the parent's pgroup does not propagate automatically. On truly
// unsupported platforms (e.g. Windows) where SysProcAttr does not exist
// or Setpgid is not a field, this function returns nil and the caller
// leaves cmd.SysProcAttr unset — belt-and-suspenders stale-PID reaping
// remains the primary defence on those platforms.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
