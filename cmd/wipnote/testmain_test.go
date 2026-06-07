//go:build !integration

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/worktree"
)

// TestMain is the test suite entry point for the non-integration (unit) test
// suite. It:
//  1. Disables the reindex subprocess fork so worktree-helper tests don't hang
//     in environments where the fork blocks on missing canonical state (bug-bb5b26f6).
//  2. Redirects XDG base dirs to isolated tempdirs so registry writes from
//     persistentPreRunE (or any code path calling registry.DefaultPath) never
//     touch ~/.local/share/wipnote/projects.json during test runs (bug-cc41e3d2).
//  3. Redirects WIPNOTE_DB_PATH to a process-scoped temp dir so that no test
//     inadvertently creates entries under the user's real ~/.cache/wipnote
//     (bug-8c34e1f5). Tests that need a per-test isolated DB can override via
//     t.Setenv("WIPNOTE_DB_PATH", ...) which restores the value afterwards.
//  4. Cleans up the binary temp dir created by buildOtelCollectTestBinary.
//
// Cleanup runs explicitly before os.Exit; deferred cleanups would never fire
// because os.Exit skips deferred functions.
func TestMain(m *testing.M) {
	worktree.SetReindexFnForTest(func(string, io.Writer) {})

	// Redirect XDG base dirs to isolated tempdirs.
	xdgData, err := os.MkdirTemp("", "wipnote-test-xdg-data-*")
	if err == nil {
		os.Setenv("XDG_DATA_HOME", xdgData) //nolint:errcheck
	}
	xdgConfig, err2 := os.MkdirTemp("", "wipnote-test-xdg-config-*")
	if err2 == nil {
		os.Setenv("XDG_CONFIG_HOME", xdgConfig) //nolint:errcheck
	}

	// Redirect DB to a process-scoped temp dir before any test runs so that
	// storage.CanonicalDBPath never touches the real user cache.
	// os.MkdirTemp is used (not t.TempDir) because TestMain has no *testing.T.
	var dbTmp string
	if tmp, err3 := os.MkdirTemp("", "wipnote-test-db-*"); err3 == nil {
		dbTmp = tmp
		os.Setenv("WIPNOTE_DB_PATH", filepath.Join(dbTmp, "wipnote.db")) //nolint:errcheck
	}

	code := m.Run()

	if xdgData != "" {
		_ = os.RemoveAll(xdgData)
	}
	if xdgConfig != "" {
		_ = os.RemoveAll(xdgConfig)
	}
	if dbTmp != "" {
		_ = os.RemoveAll(dbTmp)
	}
	if otelCollectTestBinary != "" {
		_ = os.RemoveAll(filepath.Dir(otelCollectTestBinary))
	}
	os.Exit(code)
}
