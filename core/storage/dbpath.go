// Package storage provides helpers for resolving wipnote's SQLite
// database path. The database is a derived read-index (HTML files and NDJSON
// events are canonical state); it lives in the host's OS cache directory
// rather than inside the project tree so it always sits on a filesystem
// that supports SQLite WAL/SHM mmap (ext4, APFS, etc.) regardless of how
// the project itself is mounted (virtiofs, osxfs, NFS, WSL2 DrvFs).
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// DBFileName is the canonical SQLite filename. Use the constant; never
// inline the string in callers (enforced by TestNoInlineDBPathConstruction).
const DBFileName = "wipnote.db"

// legacyHTMLGraphDBFileName is retained only so cleanup can find and warn
// about project-local SQLite files written before the wipnote rename.
const legacyHTMLGraphDBFileName = "htmlgraph.db"

// FsTypeProber is the function used to probe filesystem type for path selection.
// It returns the type name (e.g. "ext4", "tmpfs", "overlayfs", "unknown") and
// whether WAL mode is safe on that filesystem. Exported so tests can inject a
// deterministic stub.
var FsTypeProber = func(path string) (fstype string, walSafe bool) {
	return dbpkg.ProbeFsType(path)
}

// DBPathInfo carries the path-selection result including diagnostics. It is
// returned by CanonicalDBPathWithInfo so that callers (e.g. wipnote status)
// can surface the selection reason without re-running the probe.
type DBPathInfo struct {
	// Path is the selected absolute DB path.
	Path string
	// FsType is the filesystem type name of the selected path's mount.
	FsType string
	// WalSafe reports whether the selected path's filesystem supports WAL mode.
	WalSafe bool
	// Reason is a short human-readable explanation of why this path was selected.
	// Examples:
	//   "WIPNOTE_DB_PATH override"
	//   "tmpfs (volatile, preferred for WAL safety)"
	//   "ext4 (WAL safe)"
	//   "overlayfs (DELETE mode — no WAL-safe path found)"
	//   "unknown (DELETE mode — fstype probe failed, using fallback)"
	Reason string
}

// candidateRoot describes a candidate cache-root directory.
type candidateRoot struct {
	dir   string
	label string
}

// userCacheBase returns the base directory that holds wipnote's per-project
// cache (the parent of the "wipnote/" subdir).
//
// os.UserCacheDir already honors XDG_CACHE_HOME on Unix, but on macOS it
// returns ~/Library/Caches and ignores XDG_CACHE_HOME entirely. Honor an
// absolute XDG_CACHE_HOME explicitly so the cache location is overridable on
// every platform, mirroring how the local-share root honors XDG_DATA_HOME in
// candidateRoots. This keeps behavior identical on Linux (where os.UserCacheDir
// already returns an absolute XDG_CACHE_HOME) while making the cache root
// injectable on macOS. A relative XDG_CACHE_HOME is ignored — os.UserCacheDir
// treats it as an error, and we match that by falling through to it.
func userCacheBase() (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return xdg, nil
	}
	return os.UserCacheDir()
}

// candidateRoots returns the ordered list of candidate root directories for
// the DB cache. Priority:
//  1. os.UserCacheDir()  (persistent, managed by `wipnote cache prune`)
//  2. $XDG_DATA_HOME/wipnote or ~/.local/share/wipnote (persistent local-share;
//     preferred over tmp so containers with overlayfs ~/.cache keep a durable DB)
//  3. $XDG_RUNTIME_DIR (often tmpfs on Linux, tied to user session)
//  4. $TMPDIR / /tmp  (tmpfs on many Linux systems; volatile fallback)
func candidateRoots() []candidateRoot {
	var roots []candidateRoot

	if cacheDir, err := userCacheBase(); err == nil {
		roots = append(roots, candidateRoot{dir: cacheDir, label: "user-cache"})
	}

	// Persistent local-share directory: $XDG_DATA_HOME or ~/.local/share.
	// This survives container restarts when ~/.cache is on overlayfs (WAL-unsafe)
	// but ~/.local/share is on ext4 (WAL-safe and persistent).
	localShareDir := os.Getenv("XDG_DATA_HOME")
	if localShareDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			localShareDir = filepath.Join(homeDir, ".local", "share")
		}
	}
	if localShareDir != "" {
		roots = append(roots, candidateRoot{dir: localShareDir, label: "local-share"})
	}

	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		roots = append(roots, candidateRoot{dir: xdg, label: "XDG_RUNTIME_DIR"})
	}

	// Prefer $TMPDIR if set; otherwise use /tmp.
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	roots = append(roots, candidateRoot{dir: tmpDir, label: "tmp"})

	return roots
}

// projectKey computes a 16-hex-character key from the project directory
// path, after resolving symlinks. It is stable across equivalent paths.
func projectKey(projectDir string) (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve project dir: %w", err)
	}
	// Resolve symlinks so the same checkout reached via different paths
	// (e.g. macOS /var → /private/var, or a symlinked workspace) hashes
	// to one cache key. Falling back to the abs path when EvalSymlinks
	// fails (broken link, permission error) keeps the helper usable on
	// non-existent dirs that callers will create later (init flow).
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	}
	sum := sha256.Sum256([]byte(abs))
	key := hex.EncodeToString(sum[:])[:16]
	return key, nil
}

// CanonicalDBPathWithInfo returns the selected DB path together with
// filesystem diagnostics. Call this when you need to surface path-selection
// reason and fstype to users (e.g. wipnote status).
//
// Selection priority:
//  1. WIPNOTE_DB_PATH env var — always wins, never silently moved.
//  2. First candidate root whose backing filesystem is WAL-safe. tmpfs roots
//     are skipped by default because long-running OTel indexes can exhaust
//     small /tmp mounts; set WIPNOTE_ALLOW_TMPFS_DB=1 to opt in.
//  3. If no non-volatile WAL-safe path is found, fall back to UserCacheDir
//     (or /tmp) with DELETE-mode diagnostics.
func CanonicalDBPathWithInfo(projectDir string) (DBPathInfo, error) {
	// 1. Explicit override — never silently moved.
	if override := os.Getenv("WIPNOTE_DB_PATH"); override != "" {
		fstype, walSafe := FsTypeProber(override)
		return DBPathInfo{
			Path:    override,
			FsType:  fstype,
			WalSafe: walSafe,
			Reason:  "WIPNOTE_DB_PATH override",
		}, nil
	}

	key, err := projectKey(projectDir)
	if err != nil {
		return DBPathInfo{}, err
	}

	// 2. Probe candidate roots in priority order; pick first WAL-safe one.
	candidates := candidateRoots()
	skippedTmpfs := false
	for _, c := range candidates {
		if c.dir == "" {
			continue
		}
		candidatePath := filepath.Join(c.dir, "wipnote", key, DBFileName)
		fstype, walSafe := FsTypeProber(candidatePath)
		if c.label != "user-cache" && fstype == "tmpfs" && os.Getenv("WIPNOTE_ALLOW_TMPFS_DB") == "" {
			skippedTmpfs = true
			continue
		}
		if walSafe {
			reason := buildWalSafeReason(fstype, c.label)
			return DBPathInfo{
				Path:    candidatePath,
				FsType:  fstype,
				WalSafe: true,
				Reason:  reason,
			}, nil
		}
	}

	// 3. No WAL-safe candidate found. Use UserCacheDir or /tmp as deterministic
	// fallback with DELETE-mode diagnostics.
	fallbackDir, err := userCacheBase()
	if err != nil {
		if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
			fallbackDir = tmpDir
		} else {
			fallbackDir = "/tmp"
		}
	}

	fallbackPath := filepath.Join(fallbackDir, "wipnote", key, DBFileName)
	fstype, _ := FsTypeProber(fallbackPath)
	return DBPathInfo{
		Path:    fallbackPath,
		FsType:  fstype,
		WalSafe: false,
		Reason:  buildDeleteModeReason(fstype, skippedTmpfs),
	}, nil
}

// buildWalSafeReason constructs a human-readable selection reason for a
// WAL-safe path. label is the candidateRoot.label for context.
func buildWalSafeReason(fstype, label string) string {
	if fstype == "tmpfs" {
		return "tmpfs (volatile, preferred for WAL safety)"
	}
	if label == "local-share" {
		return fmt.Sprintf("%s (WAL safe, persistent local-share)", fstype)
	}
	return fmt.Sprintf("%s (WAL safe)", fstype)
}

// buildDeleteModeReason constructs a human-readable reason when no WAL-safe
// path was found. It is always explicit that DELETE mode is intentional.
func buildDeleteModeReason(fstype string, skippedTmpfs bool) string {
	if skippedTmpfs {
		return fmt.Sprintf("%s (DELETE mode — tmpfs DB disabled; set WIPNOTE_ALLOW_TMPFS_DB=1 to opt in)", fstype)
	}
	if strings.HasPrefix(fstype, "unknown") {
		return fmt.Sprintf("%s (DELETE mode — fstype probe failed, using fallback)", fstype)
	}
	return fmt.Sprintf("%s (DELETE mode — no WAL-safe filesystem found)", fstype)
}

// CanonicalDBPath returns the absolute path to the SQLite read-index for
// the given project. It selects the first WAL-safe candidate root (see
// CanonicalDBPathWithInfo for full priority). Use CanonicalDBPathWithInfo
// when you need diagnostics.
//
// Override with WIPNOTE_DB_PATH for CI, tests, or unusual setups.
// All callers MUST use this; do not construct DB paths inline.
func CanonicalDBPath(projectDir string) (string, error) {
	info, err := CanonicalDBPathWithInfo(projectDir)
	if err != nil {
		return "", err
	}
	return info.Path, nil
}

// LegacyProjectDBPaths returns the two pre-cache-migration project-local
// paths. Only the orphan-detection guard uses these; callers must not open them.
func LegacyProjectDBPaths(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, ".wipnote", DBFileName),
		filepath.Join(projectDir, ".wipnote", ".db", DBFileName),
		filepath.Join(projectDir, ".wipnote", legacyHTMLGraphDBFileName),
		filepath.Join(projectDir, ".wipnote", ".db", legacyHTMLGraphDBFileName),
	}
}

// EnsureDBDir creates the parent directory for the canonical DB if needed.
// Call once before sql.Open.
func EnsureDBDir(dbPath string) error {
	return os.MkdirAll(filepath.Dir(dbPath), 0o755)
}

// CleanLegacyDBIfSafe checks for legacy project-local SQLite files and
// handles them based on whether the canonical cache DB exists and is non-empty:
//
//   - If the canonical DB exists and has Size() > 0 (migration is complete):
//     silently os.Remove each legacy file found. Also removes the empty
//     .wipnote/.db/ directory if present and empty (using os.Remove, which
//     will not remove a non-empty directory). Removal errors are silently
//     swallowed — if a file cannot be removed, the warn branch fires instead
//     for that specific file.
//
//   - Otherwise (canonical DB missing or empty): writes a human-readable
//     warning to w for each legacy file found, so the user doesn't
//     inadvertently delete their only copy. The size is formatted as %.1f MB
//     so a 430 KB file shows as "0.4 MB" rather than "0 MB".
//
// Wire from one place that runs early in every binary path — the root
// cobra command's PersistentPreRun is the right location.
func CleanLegacyDBIfSafe(projectDir string, w io.Writer) {
	canonicalPath, err := CanonicalDBPath(projectDir)
	canonicalReady := false
	if err == nil {
		if ci, statErr := os.Stat(canonicalPath); statErr == nil && ci.Size() > 0 {
			canonicalReady = true
		}
	}

	dbDir := filepath.Join(projectDir, ".wipnote", ".db")

	// Resolve canonical path for comparison; fallback to abs if EvalSymlinks fails.
	canonicalResolved := canonicalPath
	if absPath, absErr := filepath.Abs(canonicalPath); absErr == nil {
		if resolved, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
			canonicalResolved = resolved
		} else {
			canonicalResolved = absPath
		}
	}

	var anyLegacySkipped bool
	for _, p := range LegacyProjectDBPaths(projectDir) {
		info, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		// Zero-byte files are vestigial; silently remove them.
		if info.Size() == 0 {
			_ = os.Remove(p)
			continue
		}
		if canonicalReady {
			// Guard: if canonical path refers to this same file, skip removal.
			// (User has explicitly set WIPNOTE_DB_PATH to a legacy path.)
			if legacyResolved := sameFileCheck(p, canonicalResolved); legacyResolved {
				anyLegacySkipped = true
				continue
			}
			if removeErr := os.Remove(p); removeErr == nil {
				continue
			}
			// Fall through to warn if removal fails.
		}
		rel, relErr := filepath.Rel(projectDir, p)
		if relErr != nil {
			rel = p
		}
		mb := float64(info.Size()) / (1024 * 1024)
		fmt.Fprintf(w,
			"[wipnote] WARNING: legacy SQLite file at %s (%.1f MB) is unused — DB now lives in the user cache dir. You can delete: %s\n",
			rel, mb, p)
	}

	// Remove the empty .db/ subdirectory if the canonical DB is ready and
	// the canonical path doesn't reside in .wipnote/.db/.
	if canonicalReady && !anyLegacySkipped {
		if !strings.HasPrefix(filepath.Clean(canonicalResolved), filepath.Clean(dbDir)) {
			// os.Remove succeeds only on empty directories; non-empty ones are left alone.
			_ = os.Remove(dbDir)
		}
	}
}

// sameFileCheck returns true if legacyPath (a project-local path) refers to
// the same file as canonicalResolved (an already-resolved absolute path).
// It resolves legacyPath to an absolute, evaluated form and compares cleaned paths.
func sameFileCheck(legacyPath, canonicalResolved string) bool {
	absLegacy, absErr := filepath.Abs(legacyPath)
	if absErr != nil {
		return false
	}
	resolvedLegacy := absLegacy
	if evalResolved, evalErr := filepath.EvalSymlinks(absLegacy); evalErr == nil {
		resolvedLegacy = evalResolved
	}
	return filepath.Clean(resolvedLegacy) == filepath.Clean(canonicalResolved)
}
