//go:build linux

package gate

import "golang.org/x/sys/unix"

func mountIsNoexec(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false
	}
	return st.Flags&unix.MS_NOEXEC != 0
}
