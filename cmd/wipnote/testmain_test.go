//go:build !integration

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
//  4. Redirects WIPNOTE_PROJECT_DIR and CLAUDE_PROJECT_DIR to a process-scoped
//     empty project so that no test resolves to — and writes into — the
//     developer's real repository. Tests that need their own project can
//     override via isolateProjectDir(t, dir), which layers on with t.Setenv
//     exactly as the DB-path override does.
//  5. Cleans up the binary temp dir created by buildOtelCollectTestBinary.
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

	// Redirect project-dir resolution to a process-scoped EMPTY project before
	// any test runs, so a test that reaches a command entry point can never
	// resolve to — and write into — the developer's real repository.
	//
	// This is not belt-and-braces. paths.ResolveProjectDir consults
	// WIPNOTE_PROJECT_DIR (priority 2) and CLAUDE_PROJECT_DIR (priority 3)
	// before it looks at the working directory, and both are set in any shell
	// running under a wipnote launcher — including the one running `go test`.
	// Chdir does NOT protect you, and neither does WIPNOTE_NO_AUTO_WRITER
	// above: that only stops THIS process from spawning a writer. An ambient
	// daemon already running in the session will happily pick up whatever a
	// test wrote to the real tree and commit it (this is exactly how a test
	// run put a fabricated property on ~870 real work items and got them
	// auto-committed, feat-fc3cc9e0).
	//
	// The sandbox MUST contain a real .wipnote directory. The env-var branches
	// are conditional on it existing; without it resolution falls through to
	// priority 5, git-common-dir detection from the CWD, which during
	// `go test` is cmd/wipnote — inside the real repo. An empty dir would
	// silently provide no protection at all.
	var projTmp string
	if tmp, err4 := os.MkdirTemp("", "wipnote-test-project-*"); err4 == nil {
		graphDir := filepath.Join(tmp, ".wipnote")
		if mkErr := os.MkdirAll(graphDir, 0o755); mkErr == nil {
			if subErr := createSubdirs(graphDir); subErr == nil {
				projTmp = tmp
				os.Setenv("WIPNOTE_PROJECT_DIR", tmp) //nolint:errcheck
				os.Setenv("CLAUDE_PROJECT_DIR", tmp)  //nolint:errcheck
			}
		}
		if projTmp == "" {
			_ = os.RemoveAll(tmp)
		}
	}

	// Suppress the headless-writer daemon fork for the whole unit suite:
	// workitem.Open -> SubmitOrSpawn must not spawn a background writer during
	// tests (checked at core/daemon/spawn.go). Set process-wide before m.Run.
	os.Setenv("WIPNOTE_NO_AUTO_WRITER", "1") //nolint:errcheck

	// Share ONE warm GOCACHE across the package so the gate tests' nested
	// `go test` is a build-cache hit (stdlib already compiled) instead of a
	// ~35s per-test stdlib recompile into a fresh, empty cache. We reuse the
	// ambient GOCACHE rather than pre-seeding a dedicated one: seeding would
	// add a ~35s build to TestMain on EVERY package run, defeating the
	// suite-speed goal. setupGateTestProject no longer overrides GOCACHE
	// per-test, so the nested `go test` inherits this shared cache.
	if gc := resolveSharedGOCACHE(); gc != "" {
		os.Setenv("GOCACHE", gc) //nolint:errcheck
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
	if projTmp != "" {
		_ = os.RemoveAll(projTmp)
	}
	if otelCollectTestBinary != "" {
		_ = os.RemoveAll(filepath.Dir(otelCollectTestBinary))
	}
	os.Exit(code)
}

// resolveSharedGOCACHE returns the ambient Go build-cache directory: the GOCACHE
// env var if already set, otherwise `go env GOCACHE`. Returns "" if it cannot be
// resolved, in which case callers leave GOCACHE at Go's own default. Sharing one
// warm cache is what lets the gate tests' nested `go test` hit the build cache
// instead of recompiling stdlib per test.
func resolveSharedGOCACHE() string {
	if gc := os.Getenv("GOCACHE"); gc != "" {
		return gc
	}
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestMain_EstablishesSharedGateTestEnv verifies the package-scoped environment
// TestMain sets up for the gate tests' nested `go test`: the headless-writer
// fork is disabled (WIPNOTE_NO_AUTO_WRITER=1) and a shared build cache is
// exported (GOCACHE non-empty), so the nested build is a cache hit rather than a
// ~35s stdlib recompile.
func TestMain_EstablishesSharedGateTestEnv(t *testing.T) {
	if got := os.Getenv("WIPNOTE_NO_AUTO_WRITER"); got != "1" {
		t.Errorf("WIPNOTE_NO_AUTO_WRITER = %q, want %q (TestMain must disable the headless-writer fork)", got, "1")
	}
	if os.Getenv("GOCACHE") == "" {
		t.Error("GOCACHE is empty; TestMain must export a shared build cache for the gate tests' nested go test")
	}
}

// TestMain_IsolatesProjectDirFromRealRepo is the guard on item 4 of TestMain,
// and it is the counterpart to TestNoCachePollution: it asserts the redirect
// still redirects.
//
// It deliberately resolves through findWipnoteDir — the same call every command
// entry point makes — rather than reading the env var back. Reading the var
// would prove only that TestMain set something; it would stay green if
// ResolveProjectDir's priority order changed, or if the sandbox lost its
// .wipnote directory and resolution silently fell through to git-common-dir
// detection on the real repo. The failure this guards against is invisible by
// construction, so the guard has to exercise the real path.
func TestMain_IsolatesProjectDirFromRealRepo(t *testing.T) {
	sandbox := os.Getenv("WIPNOTE_PROJECT_DIR")
	if sandbox == "" {
		t.Fatal("WIPNOTE_PROJECT_DIR is unset; TestMain must redirect project resolution away from the real repo")
	}

	resolved, err := findWipnoteDir()
	if err != nil {
		t.Fatalf("findWipnoteDir: %v", err)
	}

	wantPrefix := resolveForCompare(sandbox)
	gotRoot := resolveForCompare(filepath.Dir(resolved))
	if gotRoot != wantPrefix {
		t.Fatalf("findWipnoteDir resolved to %q, want the sandbox %q — a test that writes would hit the real repository",
			gotRoot, wantPrefix)
	}

	// The module root is where the real .wipnote lives. Resolving there is the
	// specific accident this guard exists to catch, so name it explicitly.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if realRoot := filepath.Dir(filepath.Dir(wd)); resolveForCompare(realRoot) == gotRoot {
			t.Fatalf("project resolution reached the real repository at %q", realRoot)
		}
	}
}
