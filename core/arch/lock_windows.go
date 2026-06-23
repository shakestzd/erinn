//go:build windows

package arch

func lockLedgerFile(_ string, fallbackRelease func()) func() {
	return fallbackRelease
}
