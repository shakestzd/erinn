package storage_test

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/storage"
)

// TestNoInlineDBPathConstruction walks cmd/ and internal/ (skipping the
// storage package and _test.go files) and fails when any .go file
// constructs a DB path outside storage.CanonicalDBPath. Three patterns
// are forbidden in production code outside internal/storage:
//
//  1. Lines containing the literal "wipnote.db" string. Production
//     code must reference storage.DBFileName, and even then only outside
//     a filepath.Join (rule 2).
//  2. Lines that contain BOTH filepath.Join and storage.DBFileName.
//     This catches the regression class fixed by bug-62f14f8c, where
//     internal/hooks/runner.go silently fell back to constructing
//     .wipnote/.db/wipnote.db whenever os.UserCacheDir() errored.
//     Comparison sites (e.g. `if base == storage.DBFileName`) remain
//     allowed because they do not synthesize a path.
//  3. Lines containing ".wipnote/.db" or ".db/wipnote" — the legacy
//     in-tree DB locations should never appear in callers; only
//     storage.LegacyProjectDBPaths (inside internal/storage) may
//     reference them, for the orphan-detection warning.
//
// LIMITATION: rule 2 is line-based, so a multi-line filepath.Join that
// places storage.DBFileName on its own line slips through. Keep
// filepath.Join calls single-line in production code.
func TestNoInlineDBPathConstruction(t *testing.T) {
	// Resolve module root from GOPATH or the source location.
	root := filepath.Join(build.Default.GOPATH, "src", "github.com", "shakestzd", "wipnote")
	// Fallback: walk up from this file's package to find go.mod.
	if _, err := os.Stat(root); err != nil {
		// __file__ is internal/storage/dbpath_test.go → go up three levels
		thisFile, _ := filepath.Abs("dbpath_test.go")
		root = filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	}
	// Best-effort: try the module root directly from current working dir.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		cwd, _ := os.Getwd()
		// We are in internal/storage/ — go up two dirs.
		root = filepath.Dir(filepath.Dir(cwd))
	}

	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot locate module root (tried %s); err: %v", root, err)
	}

	// Directories to scan.
	scanDirs := []string{
		filepath.Join(root, "cmd"),
		filepath.Join(root, "internal"),
	}

	// The storage package itself is the one place allowed to define DBFileName.
	storagePkg := filepath.Join(root, "internal", "storage")

	type violation struct {
		path    string
		line    int
		pattern string
	}
	// linePatterns flag a single line whenever it contains the substring.
	// filepathJoinDBFileName is checked separately because it requires both
	// substrings on the same line (legitimate comparison sites use only
	// storage.DBFileName, which is allowed).
	linePatterns := []string{
		`"wipnote.db"`,   // literal filename
		`".wipnote/.db"`, // legacy ext4-volume path segment
		`".db/wipnote"`,  // partial path hinting at legacy layout
	}

	var violations []violation
	for _, dir := range scanDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Skip the storage package — it's the definition site.
				if filepath.Clean(path) == filepath.Clean(storagePkg) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				// Test files are allowed to use WIPNOTE_DB_PATH via t.TempDir
				// and don't need the production path — skip them.
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for i, line := range strings.Split(string(data), "\n") {
				for _, pat := range linePatterns {
					if strings.Contains(line, pat) {
						violations = append(violations, violation{path: rel, line: i + 1, pattern: pat})
					}
				}
				// filepath.Join + storage.DBFileName on one line = path synthesis.
				// Comparison sites (line contains storage.DBFileName but no
				// filepath.Join) are allowed.
				if strings.Contains(line, "filepath.Join") && strings.Contains(line, "storage.DBFileName") {
					violations = append(violations, violation{
						path:    rel,
						line:    i + 1,
						pattern: "filepath.Join(...storage.DBFileName...)",
					})
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		var lines []string
		for _, v := range violations {
			lines = append(lines, fmt.Sprintf("%s:%d contains forbidden pattern %s", v.path, v.line, v.pattern))
		}
		t.Errorf("non-canonical DB-path construction outside internal/storage "+
			"(use storage.CanonicalDBPath):\n  %s", strings.Join(lines, "\n  "))
	}
}

func TestCanonicalDBPath_RespectsOverride(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "/tmp/x/y.db")
	got, err := storage.CanonicalDBPath("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/tmp/x/y.db" {
		t.Errorf("expected /tmp/x/y.db, got %s", got)
	}
}

func TestCanonicalDBPath_HashesProjectDir(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "") // ensure no override

	path1, err := storage.CanonicalDBPath("/project/alpha")
	if err != nil {
		t.Fatalf("path1 error: %v", err)
	}
	path2, err := storage.CanonicalDBPath("/project/beta")
	if err != nil {
		t.Fatalf("path2 error: %v", err)
	}
	if path1 == path2 {
		t.Error("different project dirs must produce different DB paths")
	}

	// Same dir must be stable across calls.
	path1b, err := storage.CanonicalDBPath("/project/alpha")
	if err != nil {
		t.Fatalf("path1b error: %v", err)
	}
	if path1 != path1b {
		t.Errorf("same project dir must produce stable path: %s != %s", path1, path1b)
	}
}

func TestCanonicalDBPath_DirsContainHash(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "") // ensure no override

	p, err := storage.CanonicalDBPath("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := strings.Split(filepath.ToSlash(p), "/")
	foundWipnote := false
	foundHexDir := false
	for _, seg := range parts {
		if seg == "wipnote" {
			foundWipnote = true
		}
		// 16-char lowercase hex segment
		if len(seg) == 16 {
			allHex := true
			for _, ch := range seg {
				if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
					allHex = false
					break
				}
			}
			if allHex {
				foundHexDir = true
			}
		}
	}
	if !foundWipnote {
		t.Errorf("expected 'wipnote' segment in path %s", p)
	}
	if !foundHexDir {
		t.Errorf("expected 16-char hex segment in path %s", p)
	}
}

func TestLegacyProjectDBPaths(t *testing.T) {
	projectDir := "/my/project"
	paths := storage.LegacyProjectDBPaths(projectDir)

	if len(paths) != 4 {
		t.Fatalf("expected 4 legacy paths, got %d", len(paths))
	}

	want := []string{
		filepath.Join(projectDir, ".wipnote", "wipnote.db"),
		filepath.Join(projectDir, ".wipnote", ".db", "wipnote.db"),
		// The old filename remains detectable for cleanup after the rename.
		filepath.Join(projectDir, ".wipnote", "htmlgraph.db"),
		filepath.Join(projectDir, ".wipnote", ".db", "htmlgraph.db"),
	}

	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("path[%d]: got %s, want %s", i, paths[i], want[i])
		}
	}
}

// TestCleanLegacyDBIfSafe_DeletesWhenCanonicalReady verifies that when the
// canonical DB exists and is non-empty, the legacy file is silently deleted
// and no output is written.
func TestCleanLegacyDBIfSafe_DeletesWhenCanonicalReady(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Set up legacy file.
	legacyDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "wipnote.db")
	if err := os.WriteFile(legacyFile, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	// No output expected.
	if buf.Len() != 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}

	// Legacy file must be gone.
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Errorf("expected legacy file to be removed, but it still exists")
	}
}

// TestCleanLegacyDBIfSafe_WarnsWhenCanonicalMissing verifies that when the
// canonical DB does not exist, the legacy file is NOT deleted and a warning
// with %.1f MB formatting is written.
func TestCleanLegacyDBIfSafe_WarnsWhenCanonicalMissing(t *testing.T) {
	projectDir := t.TempDir()

	// Point canonical DB to a path that does not exist.
	canonicalPath := filepath.Join(projectDir, "nonexistent-canonical.db")
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Set up legacy file (~430 KB so it shows as 0.4 MB, not 0 MB).
	legacyDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "wipnote.db")
	content := make([]byte, 440*1024) // 440 KB
	if err := os.WriteFile(legacyFile, content, 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	output := buf.String()
	if output == "" {
		t.Error("expected warning output, got nothing")
	}
	// Must contain the decimal MB format — "0.4" not "0 MB".
	if !strings.Contains(output, "0.4") {
		t.Errorf("expected '0.4' in MB display, got: %q", output)
	}
	if strings.Contains(output, "0 MB") {
		t.Errorf("must not display '0 MB' for a non-zero file; got: %q", output)
	}

	// Legacy file must still be present.
	if _, err := os.Stat(legacyFile); err != nil {
		t.Errorf("expected legacy file to remain, but got: %v", err)
	}
}

// TestCleanLegacyDBIfSafe_WarnsWhenCanonicalEmpty verifies that when the
// canonical DB file exists but is empty (Size() == 0), the legacy file is
// NOT deleted and a warning is written.
func TestCleanLegacyDBIfSafe_WarnsWhenCanonicalEmpty(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB that is empty (size 0).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write empty canonical db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Set up legacy file.
	legacyDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "wipnote.db")
	if err := os.WriteFile(legacyFile, []byte("stale data"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	output := buf.String()
	if output == "" {
		t.Error("expected warning output when canonical DB is empty, got nothing")
	}

	// Legacy file must still be present.
	if _, err := os.Stat(legacyFile); err != nil {
		t.Errorf("expected legacy file to remain, but got: %v", err)
	}
}

// TestCleanLegacyDBIfSafe_RemovesEmptyDBDir verifies that the empty
// .wipnote/.db/ directory is removed when the canonical DB is non-empty.
func TestCleanLegacyDBIfSafe_RemovesEmptyDBDir(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Create empty .wipnote/.db/ directory (no legacy DB file inside).
	dbDir := filepath.Join(projectDir, ".wipnote", ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote/.db: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	// No output expected.
	if buf.Len() != 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}

	// Empty .db/ directory must be removed.
	if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
		t.Errorf("expected empty .db/ dir to be removed, but it still exists")
	}
}

// TestCleanLegacyDBIfSafe_LeavesNonEmptyDBDir verifies that a non-empty
// .wipnote/.db/ directory (containing unrelated files) is NOT removed.
func TestCleanLegacyDBIfSafe_LeavesNonEmptyDBDir(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Create .wipnote/.db/ with an unrelated file inside.
	dbDir := filepath.Join(projectDir, ".wipnote", ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote/.db: %v", err)
	}
	unrelated := filepath.Join(dbDir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	// Non-empty .db/ directory must still be present.
	if _, err := os.Stat(dbDir); err != nil {
		t.Errorf("expected non-empty .db/ dir to remain, but got: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("expected unrelated file to remain, but got: %v", err)
	}
}

// TestCleanLegacyDBIfSafe_NoLegacyFiles verifies that no-op behavior
// (no output, no errors) when no legacy files are present.
func TestCleanLegacyDBIfSafe_NoLegacyFiles(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// No .wipnote/ directory or legacy files created.

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no output when no legacy files exist, got: %q", buf.String())
	}
}

// TestCleanLegacyDBIfSafe_NoLegacyFilesAndNoCanonical verifies that when
// neither legacy files nor a canonical DB exist (brand new user), no output
// is produced. This is the scenario that was filing bug-5b5611c2.
func TestCleanLegacyDBIfSafe_NoLegacyFilesAndNoCanonical(t *testing.T) {
	projectDir := t.TempDir()

	// Do NOT set WIPNOTE_DB_PATH — let CanonicalDBPath compute it.
	// Do NOT create any files — new user with nothing.
	t.Setenv("WIPNOTE_DB_PATH", "") // ensure no override

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no output when no legacy files and no canonical DB exist, got: %q", buf.String())
	}
}

// TestCleanLegacyDBIfSafe_DeletesZeroByteFile verifies that zero-byte legacy
// files are silently removed, even when the canonical DB is not yet ready.
// This prevents spurious warnings for vestigial/orphaned zero-byte files.
func TestCleanLegacyDBIfSafe_DeletesZeroByteFile(t *testing.T) {
	projectDir := t.TempDir()

	// Point canonical DB to a path that does not exist (canonical not ready).
	canonicalPath := filepath.Join(projectDir, "nonexistent-canonical.db")
	t.Setenv("WIPNOTE_DB_PATH", canonicalPath)

	// Create a zero-byte legacy file (vestigial).
	legacyDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "wipnote.db")
	if err := os.WriteFile(legacyFile, []byte{}, 0o600); err != nil {
		t.Fatalf("write zero-byte legacy db: %v", err)
	}

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	// No output expected (zero-byte file is silently removed).
	if buf.Len() != 0 {
		t.Errorf("expected no output for zero-byte legacy file, got: %q", buf.String())
	}

	// Zero-byte file must be removed.
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Errorf("expected zero-byte legacy file to be removed, but it still exists")
	}
}

// TestCleanLegacyDBIfSafe_WIPNOTE_DB_PATH_PointingAtLegacy verifies that when
// WIPNOTE_DB_PATH is explicitly set to a legacy path (e.g. .wipnote/wipnote.db),
// that file is NOT deleted and the .db/ directory is also protected.
func TestCleanLegacyDBIfSafe_WIPNOTE_DB_PATH_PointingAtLegacy(t *testing.T) {
	projectDir := t.TempDir()

	// Set up the legacy path as the canonical DB via WIPNOTE_DB_PATH.
	legacyDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "wipnote.db")
	if err := os.WriteFile(legacyFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	t.Setenv("WIPNOTE_DB_PATH", legacyFile)

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	// Legacy file must still exist (not deleted).
	if _, err := os.Stat(legacyFile); err != nil {
		t.Errorf("expected legacy file to remain, but got: %v", err)
	}

	// No output expected (it's the canonical, no warning).
	if buf.Len() != 0 {
		t.Errorf("expected no output when WIPNOTE_DB_PATH points at legacy file, got: %q", buf.String())
	}
}

// ---- WAL-safe path selection tests (Slice 4) ----

// TestCanonicalDBPath_PrefersUserCacheWhenWalSafe verifies the default path is
// managed by the cache-pruning system instead of volatile /tmp when both are
// WAL-safe.
func TestCanonicalDBPath_PrefersUserCacheWhenWalSafe(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(path string) (string, bool) {
		if strings.HasPrefix(path, cacheBase) {
			return "ext4", true
		}
		if strings.HasPrefix(path, tmpBase) {
			return "tmpfs", true
		}
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(info.Path, cacheBase) {
		t.Errorf("expected path under cacheBase %q, got %q", cacheBase, info.Path)
	}
	if !info.WalSafe {
		t.Errorf("expected WalSafe=true for cache candidate")
	}
	if info.FsType != "ext4" {
		t.Errorf("expected FsType=ext4, got %q", info.FsType)
	}
	if strings.Contains(info.Path, tmpBase) {
		t.Errorf("must not choose volatile tmp path when cache is WAL-safe: %q", info.Path)
	}
}

func TestCanonicalDBPath_SkipsTmpfsByDefaultWhenCacheUnsafe(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("WIPNOTE_ALLOW_TMPFS_DB", "")

	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(path string) (string, bool) {
		if strings.HasPrefix(path, cacheBase) {
			return "overlayfs", false
		}
		if strings.HasPrefix(path, tmpBase) {
			return "tmpfs", true
		}
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(info.Path, cacheBase) {
		t.Errorf("expected path under cacheBase %q when tmpfs is skipped, got %q", cacheBase, info.Path)
	}
	if info.WalSafe || !strings.Contains(info.Reason, "tmpfs DB disabled") {
		t.Errorf("expected DELETE-mode cache fallback, got reason=%q walSafe=%v", info.Reason, info.WalSafe)
	}
}

func TestCanonicalDBPath_AllowsTmpfsWithOptIn(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("WIPNOTE_ALLOW_TMPFS_DB", "1")

	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(path string) (string, bool) {
		if strings.HasPrefix(path, cacheBase) {
			return "overlayfs", false
		}
		if strings.HasPrefix(path, tmpBase) {
			return "tmpfs", true
		}
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(info.Path, tmpBase) {
		t.Errorf("expected opted-in tmpfs path under %q, got %q", tmpBase, info.Path)
	}
	if !info.WalSafe || info.FsType != "tmpfs" {
		t.Errorf("expected tmpfs WAL-safe fallback, got fstype=%q walSafe=%v", info.FsType, info.WalSafe)
	}
}

// TestCanonicalDBPath_OverrideWins verifies that WIPNOTE_DB_PATH is returned
// verbatim regardless of what the fstype probe returns.
func TestCanonicalDBPath_OverrideWins(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "/custom/path/wipnote.db")

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	// Even if we inject a probe that always says unsafe, the override must win.
	storage.FsTypeProber = func(path string) (string, bool) {
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/any/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Path != "/custom/path/wipnote.db" {
		t.Errorf("expected override path, got %q", info.Path)
	}
	if info.Reason != "WIPNOTE_DB_PATH override" {
		t.Errorf("expected reason=WIPNOTE_DB_PATH override, got %q", info.Reason)
	}

	// CanonicalDBPath (non-info variant) must also respect the override.
	got, err := storage.CanonicalDBPath("/any/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/custom/path/wipnote.db" {
		t.Errorf("CanonicalDBPath: expected override, got %q", got)
	}
}

// TestCanonicalDBPath_UnknownProbeDeterministic injects a probe that always
// returns ("unknown", false). The function must return a deterministic path and
// emit an "unknown" fstype in diagnostics.
func TestCanonicalDBPath_UnknownProbeDeterministic(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(_ string) (string, bool) {
		return "unknown", false
	}

	info1, err := storage.CanonicalDBPathWithInfo("/project/alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Call a second time — must return the same path (deterministic).
	info2, err := storage.CanonicalDBPathWithInfo("/project/alpha")
	if err != nil {
		t.Fatalf("unexpected error (second call): %v", err)
	}
	if info1.Path != info2.Path {
		t.Errorf("path not deterministic: %q vs %q", info1.Path, info2.Path)
	}
	// Diagnostics must flag unknown.
	if !strings.Contains(info1.FsType, "unknown") {
		t.Errorf("expected FsType to contain 'unknown', got %q", info1.FsType)
	}
	if !strings.Contains(info1.Reason, "unknown") {
		t.Errorf("expected Reason to mention 'unknown', got %q", info1.Reason)
	}
	// WalSafe must be false.
	if info1.WalSafe {
		t.Errorf("expected WalSafe=false for unknown fstype")
	}
}

// TestCanonicalDBPath_AllUnsafeFallsBackToUserCache verifies that when all
// candidate roots are unsafe, the function falls back deterministically to a
// path under UserCacheDir and the reason mentions DELETE mode.
func TestCanonicalDBPath_AllUnsafeFallsBackToUserCache(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(_ string) (string, bool) {
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/project/alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(info.Reason, "DELETE mode") {
		t.Errorf("expected reason to mention DELETE mode, got %q", info.Reason)
	}
	if info.WalSafe {
		t.Errorf("expected WalSafe=false for all-unsafe scenario")
	}
	// Path must end with the canonical DB filename.
	if filepath.Base(info.Path) != storage.DBFileName {
		t.Errorf("expected path to end with %q, got %q", storage.DBFileName, info.Path)
	}
}

// TestDBPathInfo_Fields verifies that DBPathInfo fields are populated from
// CanonicalDBPathWithInfo.
func TestDBPathInfo_Fields(t *testing.T) {
	t.Setenv("WIPNOTE_DB_PATH", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	origProber := storage.FsTypeProber
	t.Cleanup(func() { storage.FsTypeProber = origProber })
	storage.FsTypeProber = func(path string) (string, bool) {
		if strings.HasPrefix(path, tmpBase) {
			return "ext4", true
		}
		return "overlayfs", false
	}

	info, err := storage.CanonicalDBPathWithInfo("/my/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Path == "" {
		t.Error("Path must not be empty")
	}
	if info.FsType == "" {
		t.Error("FsType must not be empty")
	}
	if info.Reason == "" {
		t.Error("Reason must not be empty")
	}
	if filepath.Base(info.Path) != storage.DBFileName {
		t.Errorf("Path must end with %q, got %q", storage.DBFileName, info.Path)
	}
}
