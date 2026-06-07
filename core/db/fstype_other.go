//go:build !linux

package db

// ProbeFsType returns the filesystem type name and WAL-safety for the directory
// containing path. On non-Linux platforms (macOS, Windows), native filesystems
// (APFS, HFS+, NTFS) support WAL mmap correctly, so we return a descriptive
// name and mark them as safe. The actual type name is not probed — we return
// "native" as a sentinel for the diagnostic output.
func ProbeFsType(_ string) (fstype string, walSafe bool) {
	return "native", true
}

// isUnsafeForMmap returns false on non-Linux platforms. Native filesystems on
// macOS (APFS, HFS+) and Windows (NTFS) support mmap correctly, so WAL mode
// is safe to use.
func isUnsafeForMmap(_ string) bool {
	return false
}
