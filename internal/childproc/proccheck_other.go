//go:build !linux

package childproc

// isServeChildPID returns true on non-Linux platforms without /proc.
// Pdeathsig is not available on these platforms either, so the stale-
// reaper acts as best-effort cleanup. We accept the theoretical risk of
// killing an unrelated PID-recycled process in exchange for actually
// cleaning up orphans when they exist.
func isServeChildPID(_ int, _ string) bool {
	return true
}
