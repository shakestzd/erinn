//go:build !linux && !darwin

package collector

// readProcStartTime has no implementation on this platform. Returns
// ok=false unconditionally, which causes IsCollectorAlive to fall back to
// the PID-only Signal(0) probe (no PID-reuse protection) — see that
// function's doc comment. wipnote's launch/dev targets are Linux and
// Darwin (see procstart_linux.go, procstart_darwin.go); this stub exists
// so the package still builds elsewhere rather than to assert those
// platforms are supported.
func readProcStartTime(pid int) (uint64, bool) {
	return 0, false
}
