//go:build !integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/storage"
)

// TestNoCachePollution is a regression test for bug-8c34e1f5.
//
// Each call to storage.CanonicalDBPath with a unique project dir would
// previously create a fresh subdirectory under the user's real
// ~/.cache/wipnote, causing thousands of cache entries and gigabytes of
// disk usage after a single test run. TestMain (testmain_test.go) now sets
// WIPNOTE_DB_PATH to a process-scoped temp dir before tests run, so
// CanonicalDBPath always returns the override and never touches the real cache.
//
// This test verifies that invariant: after calling CanonicalDBPath with
// several unique project dirs, the real OS cache dir has gained zero new
// subdirectories.
func TestNoCachePollution(t *testing.T) {
	// Get the real user cache dir. Because TestMain has already set
	// WIPNOTE_DB_PATH, we must read the cache location directly from the OS
	// rather than going through CanonicalDBPath.
	realCacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("os.UserCacheDir() unavailable: %v", err)
	}
	wipnoteCacheDir := filepath.Join(realCacheDir, "wipnote")

	// Count existing subdirs in the real cache before the test.
	before := countSubdirs(t, wipnoteCacheDir)

	// Call CanonicalDBPath with several distinct project dirs to exercise the
	// path that used to create cache entries. Each unique dir gets a different
	// hash, so without the env-var redirect this would create N new subdirs.
	for i := range 5 {
		projectDir := t.TempDir()
		_ = i // ensure loop runs 5 times with different dirs
		got, err := storage.CanonicalDBPath(projectDir)
		if err != nil {
			t.Fatalf("CanonicalDBPath(%q): %v", projectDir, err)
		}
		// Verify the returned path is NOT under the real OS cache dir —
		// it should be under the temp dir set by TestMain.
		if strings.HasPrefix(got, wipnoteCacheDir) {
			t.Errorf("CanonicalDBPath(%q) = %q: path is under real cache dir %q (WIPNOTE_DB_PATH not set?)",
				projectDir, got, wipnoteCacheDir)
		}
	}

	// Count subdirs after — must not have grown.
	after := countSubdirs(t, wipnoteCacheDir)
	if after > before {
		t.Errorf("real cache dir %q grew by %d subdirs during test (before=%d after=%d): WIPNOTE_DB_PATH redirect is not working",
			wipnoteCacheDir, after-before, before, after)
	}
}

// countSubdirs returns the number of immediate subdirectories in dir.
// Returns 0 if dir does not exist (cache dir may not exist on a fresh machine).
func countSubdirs(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var count int
	for _, e := range entries {
		if e.IsDir() {
			count++
		}
	}
	return count
}
