package storage_test

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/htmlgraph/internal/storage"
)

// TestNoInlineDBPathConstruction walks cmd/ and internal/ (skipping the
// storage package and _test.go files) and fails if any .go file contains
// the literal "htmlgraph.db" outside the storage package.  This enforces
// that every future caller goes through storage.CanonicalDBPath /
// storage.DBFileName rather than constructing the path inline.
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

	var violations []string
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
			if strings.Contains(string(data), `"htmlgraph.db"`) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("files contain inline %q (use storage.DBFileName or storage.CanonicalDBPath):\n  %s",
			"htmlgraph.db", strings.Join(violations, "\n  "))
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
