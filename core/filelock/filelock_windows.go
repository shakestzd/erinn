//go:build windows

package filelock

// CrossProcess is false on Windows: no cross-process lock is taken, so two
// wipnote processes on one repository can still interleave a read-modify-write
// on the same canonical file. This is the known, filed gap bug-68f3593b — it
// is surfaced as a constant so dependent code states the dependency instead of
// silently assuming exclusion it does not have.
const CrossProcess = false

func lockSidecar(_ string, fallbackRelease func()) func() {
	return fallbackRelease
}
