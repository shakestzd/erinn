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

// WarnIfLegacyDBPresent checks whether any legacy project-local SQLite
// files are still present and writes a human-readable warning to w for
// each one found. It never deletes files or blocks startup.
//
// Wire from one place that runs early in every binary path — the root
// cobra command's PersistentPreRun is the right location.
func WarnIfLegacyDBPresent(projectDir string, w io.Writer) {
	for _, p := range LegacyProjectDBPaths(projectDir) {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(projectDir, p)
		if err != nil {
			rel = p
		}
		mb := float64(info.Size()) / (1024 * 1024)
		fmt.Fprintf(w,
			"[htmlgraph] WARNING: legacy SQLite file at %s (%.0f MB) is unused — DB now lives in the user cache dir. You can delete: %s\n",
			rel, mb, p)
	}
}
