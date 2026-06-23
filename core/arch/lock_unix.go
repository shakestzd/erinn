//go:build !windows

package arch

import (
	"os"
	"syscall"
)

func lockLedgerFile(ledgerPath string, fallbackRelease func()) func() {
	lockPath := ledgerPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
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
