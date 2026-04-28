// Package storage provides helpers for resolving HtmlGraph's SQLite
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
)

// DBFileName is the canonical SQLite filename. Use the constant; never
// inline the string in callers (enforced by TestNoInlineDBPathConstruction).
const DBFileName = "htmlgraph.db"

// CanonicalDBPath returns the absolute path to the SQLite read-index for
// the given project. The DB lives in the host's OS cache directory keyed
// by project-path hash — never inside the project tree — so it always
// sits on a filesystem that supports SQLite WAL/SHM mmap (ext4, APFS, etc.)
// regardless of how the project itself is mounted (virtiofs, osxfs, NFS).
//
// SQLite is a derived index in HtmlGraph: HTML files and NDJSON events
// are the canonical store. Losing the cache file is harmless — the
// indexer rebuilds it.
//
// Override with HTMLGRAPH_DB_PATH for CI, tests, or unusual setups.
// All callers MUST use this; do not construct DB paths inline.
func CanonicalDBPath(projectDir string) (string, error) {
	if override := os.Getenv("HTMLGRAPH_DB_PATH"); override != "" {
		return override, nil
	}
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
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	sum := sha256.Sum256([]byte(abs))
	key := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(cache, "htmlgraph", key, DBFileName), nil
}

// LegacyProjectDBPaths returns the two pre-cache-migration project-local
// paths. Only the orphan-detection guard uses these; callers must not open them.
func LegacyProjectDBPaths(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, ".htmlgraph", DBFileName),
		filepath.Join(projectDir, ".htmlgraph", ".db", DBFileName),
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
//     .htmlgraph/.db/ directory if present and empty (using os.Remove, which
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

	dbDir := filepath.Join(projectDir, ".htmlgraph", ".db")

	for _, p := range LegacyProjectDBPaths(projectDir) {
		info, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		if canonicalReady {
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
			"[htmlgraph] WARNING: legacy SQLite file at %s (%.1f MB) is unused — DB now lives in the user cache dir. You can delete: %s\n",
			rel, mb, p)
	}

	// Remove the empty .db/ subdirectory if the canonical DB is ready.
	if canonicalReady {
		// os.Remove succeeds only on empty directories; non-empty ones are left alone.
		_ = os.Remove(dbDir)
	}
}
