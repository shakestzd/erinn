//go:build linux

package main

import "golang.org/x/sys/unix"

// mountIsNoexec reports whether the filesystem backing dir is mounted with the
// MS_NOEXEC flag, using statfs. A false result is non-authoritative (the caller
// confirms with a write+exec probe); a true result is a fast, reliable reject.
func mountIsNoexec(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false
	}
	return st.Flags&unix.ST_NOEXEC != 0
}
