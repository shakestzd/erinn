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
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
}

// WriterSysProcAttr returns the SysProcAttr for a serve-MANAGED headless writer
// daemon (feat-075c110d increment 2). Unlike the detached CLI/hook auto-spawn
// (internal/daemon/spawn.go, which must OUTLIVE its short-lived spawner and so
// omits Pdeathsig), a writer started by `wipnote serve` must be reaped when its
// serve_child dies — so it gets the same Setpgid + Pdeathsig as a supervised
// HTTP child. Setpgid isolates it from the parent's pgroup; Pdeathsig delivers
// SIGTERM the moment serve_child exits.
func WriterSysProcAttr() *syscall.SysProcAttr {
	return childSysProcAttr()
}
