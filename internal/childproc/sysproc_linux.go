//go:build linux

package childproc

import "syscall"

// childSysProcAttr returns the platform-specific SysProcAttr for child
// processes. On Linux, Setpgid isolates the child in its own process group
// (so a SIGKILL to the parent's process group does not propagate) and
// Pdeathsig asks the kernel to deliver SIGTERM to the child automatically
// the moment the parent process dies — the primary defense against orphaned
// _serve-child processes holding the SQLite write lock.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:  true,
		Pdeathsig: syscall.SIGTERM,
	}
}
