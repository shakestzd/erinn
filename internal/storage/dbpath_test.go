package storage_test

import (
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/htmlgraph/internal/storage"
)

// TestNoInlineDBPathConstruction walks cmd/ and internal/ (skipping the
// storage package and _test.go files) and fails when any .go file
// constructs a DB path outside storage.CanonicalDBPath. Three patterns
// are forbidden in production code outside internal/storage:
//
//  1. Lines containing the literal "htmlgraph.db" string. Production
//     code must reference storage.DBFileName, and even then only outside
//     a filepath.Join (rule 2).
//  2. Lines that contain BOTH filepath.Join and storage.DBFileName.
//     This catches the regression class fixed by bug-62f14f8c, where
//     internal/hooks/runner.go silently fell back to constructing
//     .htmlgraph/.db/htmlgraph.db whenever os.UserCacheDir() errored.
//     Comparison sites (e.g. `if base == storage.DBFileName`) remain
//     allowed because they do not synthesize a path.
//  3. Lines containing ".htmlgraph/.db" or ".db/htmlgraph" — the legacy
//     in-tree DB locations should never appear in callers; only
//     storage.LegacyProjectDBPaths (inside internal/storage) may
//     reference them, for the orphan-detection warning.
//
// LIMITATION: rule 2 is line-based, so a multi-line filepath.Join that
// places storage.DBFileName on its own line slips through. Keep
// filepath.Join calls single-line in production code.
func TestNoInlineDBPathConstruction(t *testing.T) {
	// Resolve module root from GOPATH or the source location.
	root := filepath.Join(build.Default.GOPATH, "src", "github.com", "shakestzd", "htmlgraph")
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
		`"htmlgraph.db"`,   // literal filename
		`".htmlgraph/.db"`, // legacy ext4-volume path segment
		`".db/htmlgraph"`,  // partial path hinting at legacy layout
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
				// Test files are allowed to use HTMLGRAPH_DB_PATH via t.TempDir
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
	t.Setenv("HTMLGRAPH_DB_PATH", "/tmp/x/y.db")
	got, err := storage.CanonicalDBPath("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/tmp/x/y.db" {
		t.Errorf("expected /tmp/x/y.db, got %s", got)
	}
}

func TestCanonicalDBPath_HashesProjectDir(t *testing.T) {
	t.Setenv("HTMLGRAPH_DB_PATH", "") // ensure no override

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
	t.Setenv("HTMLGRAPH_DB_PATH", "") // ensure no override

	p, err := storage.CanonicalDBPath("/some/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := strings.Split(filepath.ToSlash(p), "/")
	foundHtmlgraph := false
	foundHexDir := false
	for _, seg := range parts {
		if seg == "htmlgraph" {
			foundHtmlgraph = true
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
	if !foundHtmlgraph {
		t.Errorf("expected 'htmlgraph' segment in path %s", p)
	}
	if !foundHexDir {
		t.Errorf("expected 16-char hex segment in path %s", p)
	}
}

func TestLegacyProjectDBPaths(t *testing.T) {
	projectDir := "/my/project"
	paths := storage.LegacyProjectDBPaths(projectDir)

	if len(paths) != 2 {
		t.Fatalf("expected 2 legacy paths, got %d", len(paths))
	}

	want0 := filepath.Join(projectDir, ".htmlgraph", "htmlgraph.db")
	want1 := filepath.Join(projectDir, ".htmlgraph", ".db", "htmlgraph.db")

	if paths[0] != want0 {
		t.Errorf("path[0]: got %s, want %s", paths[0], want0)
	}
	if paths[1] != want1 {
		t.Errorf("path[1]: got %s, want %s", paths[1], want1)
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
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// Set up legacy file.
	legacyDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "htmlgraph.db")
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
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// Set up legacy file (~430 KB so it shows as 0.4 MB, not 0 MB).
	legacyDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "htmlgraph.db")
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
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// Set up legacy file.
	legacyDir := filepath.Join(projectDir, ".htmlgraph")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph: %v", err)
	}
	legacyFile := filepath.Join(legacyDir, "htmlgraph.db")
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
// .htmlgraph/.db/ directory is removed when the canonical DB is non-empty.
func TestCleanLegacyDBIfSafe_RemovesEmptyDBDir(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// Create empty .htmlgraph/.db/ directory (no legacy DB file inside).
	dbDir := filepath.Join(projectDir, ".htmlgraph", ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph/.db: %v", err)
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
// .htmlgraph/.db/ directory (containing unrelated files) is NOT removed.
func TestCleanLegacyDBIfSafe_LeavesNonEmptyDBDir(t *testing.T) {
	projectDir := t.TempDir()

	// Set up canonical DB (non-empty).
	canonicalPath := filepath.Join(projectDir, "canonical.db")
	if err := os.WriteFile(canonicalPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write canonical db: %v", err)
	}
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// Create .htmlgraph/.db/ with an unrelated file inside.
	dbDir := filepath.Join(projectDir, ".htmlgraph", ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .htmlgraph/.db: %v", err)
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
	t.Setenv("HTMLGRAPH_DB_PATH", canonicalPath)

	// No .htmlgraph/ directory or legacy files created.

	var buf strings.Builder
	storage.CleanLegacyDBIfSafe(projectDir, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no output when no legacy files exist, got: %q", buf.String())
	}
}
