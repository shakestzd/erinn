//go:build !linux

package daemon

// isWriterProcessImpl on non-Linux platforms returns true (trusts the kill -0
// liveness check in leaseOwnerAlive). Full cmdline verification requires
// /proc which is Linux-specific; other platforms rely on the liveness check
// as the primary defence against PID reuse.
func isWriterProcessImpl(pid int, leasePath string) bool {
	// On darwin, FreeBSD, and other POSIX systems without /proc, we trust the
	// kill(pid, 0) liveness check and skip cmdline verification.
	return true
}
