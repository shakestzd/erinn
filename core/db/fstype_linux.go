//go:build linux

package db

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Filesystem magic numbers for filesystems where WAL-SHM mmap is unsafe.
const (
	fuseSuperMagic      = 0x65735546
	v9fsMagic           = 0x01021997 // 9p / virtiofs on older kernels
	overlayFSSuperMagic = 0x794c7630
	nfsSuperMagic       = 0x6969
	smbSuperMagic       = 0xFE534D42
	smb2SuperMagic      = 0x517B

	// Safelisted native filesystem magic numbers (WAL is safe).
	ext4Magic  = 0xEF53
	xfsMagic   = 0x58465342
	btrfsMagic = 0x9123683E
	tmpfsMagic = 0x01021994
	zfsMagic   = 0x2FC12FC1
)

// ProbeFsType returns the filesystem type name and whether it is WAL-safe for
// the directory containing path. WAL-safe means SQLite WAL-mode mmap will work
// correctly (no SIGBUS risk). Unsafe filesystems include virtiofs, FUSE, 9p,
// overlayfs, NFS, and SMB.
//
// If the directory does not exist yet, ProbeFsType walks up the path to find
// the nearest existing ancestor and probes that. This handles paths that have
// not been created yet (e.g. the target DB directory before first open).
//
// Returns ("unknown", false) when the filesystem type cannot be determined
// (e.g. statfs fails on all ancestors). "unknown" is deterministic — callers
// should emit fstype=unknown in diagnostics and treat it as unsafe.
func ProbeFsType(path string) (fstype string, walSafe bool) {
	// Start from the parent directory of the target path.
	dir := filepath.Dir(path)
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}

	// Walk up to find the nearest existing ancestor directory.
	probe := dir
	for {
		var buf unix.Statfs_t
		if err := unix.Statfs(probe, &buf); err == nil {
			return classifyFsType(buf.Type)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Reached filesystem root without success.
			break
		}
		probe = parent
	}
	return "unknown", false
}

// classifyFsType maps a statfs f_type magic to a name and WAL-safety bool.
func classifyFsType(magic int64) (fstype string, walSafe bool) {
	switch magic {
	case fuseSuperMagic:
		return "fuse", false
	case v9fsMagic:
		return "virtiofs", false
	case overlayFSSuperMagic:
		return "overlayfs", false
	case nfsSuperMagic:
		return "nfs", false
	case smbSuperMagic:
		return "smb", false
	case smb2SuperMagic:
		return "smb2", false
	case ext4Magic:
		return "ext4", true
	case xfsMagic:
		return "xfs", true
	case btrfsMagic:
		return "btrfs", true
	case tmpfsMagic:
		return "tmpfs", true
	case zfsMagic:
		return "zfs", true
	default:
		return fmt.Sprintf("unknown(0x%X)", uint64(magic)), false
	}
}

// isUnsafeForMmap returns true if the filesystem backing dbPath is known to be
// unsafe for SQLite WAL-mode mmap (e.g. virtiofs, FUSE, 9p, overlayfs, NFS,
// SMB). Returns false on safelisted native filesystems (ext4, xfs, btrfs,
// tmpfs, zfs). Unknown filesystems return true — prefer correctness over speed.
func isUnsafeForMmap(dbPath string) bool {
	_, walSafe := ProbeFsType(dbPath)
	return !walSafe
}
