//go:build !windows

package filelock

import (
	"os"
	"path/filepath"
	"syscall"
)

// CrossProcess reports whether Guard actually excludes other PROCESSES, not
// just other goroutines. It is true here (flock) and false on Windows, where
// the equivalent lock has never been implemented (bug-68f3593b). Code that is
// only correct under cross-process exclusion must branch on this constant or
// document the gap explicitly — it must not silently assume the lock works.
const CrossProcess = true

// lockSidecar takes a blocking LOCK_EX flock on "<path>.lock" and returns a
// release that unlocks, closes, and then runs fallbackRelease (the in-process
// unlock). If the sidecar cannot be opened or locked, the in-process mutex is
// still held, so the caller degrades to single-process serialisation rather
// than to no serialisation at all.
func lockSidecar(path string, fallbackRelease func()) func() {
	// The sidecar lives beside the guarded file, so its directory must exist
	// before the very first write to a brand-new artifact. Without this the
	// open fails and the guard silently degrades to in-process only.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fallbackRelease
	}
	f, err := os.OpenFile(path+LockSuffix, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fallbackRelease
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return fallbackRelease
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		fallbackRelease()
	}
}
