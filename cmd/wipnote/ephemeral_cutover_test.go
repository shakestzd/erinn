package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNormalRuntimeSQLiteOpenBoundary(t *testing.T) {
	for _, path := range []string{
		"main.go",
		"session.go",
		"serve_child.go",
		// was lazy_reindex.go — renamed with the removal of the cold-clone
		// lazy-rebuild machinery, which had no question left to answer once
		// every projection started empty (feat-fc3cc9e0).
		"projection_hydrate.go",
		"projects.go",
		"trace.go",
		"related.go",
		"arch_cmds.go",
		"recap_list.go",
		"plan_yaml_cmds.go",
		"plan_interview.go",
		"plan_typed_sections.go",
		"track.go",
		// CLI commands migrated off the persistent project DB (feat-fc3cc9e0).
		// Each of these used to resolve storage.CanonicalDBPath and open a
		// file-backed handle; they now read canonical artifacts, derive from
		// git, or use a process-local projection.
		"cache.go",
		"codex.go",
		"ingest_gemini.go",
		"link_commit.go",
		"migrate.go",
		"migrate_normalize.go",
		"prune.go",
		"purge_spikes.go",
		"reindex_orphans.go",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(data)
		for _, forbidden := range []string{
			"CanonicalDBPath(",
			"EnsureDBDir(",
			"OpenReadOnly(",
			"OpenReadOnlyMigrated(",
			"OpenReadOnlyFast(",
			"OpenWritable(",
			"dbpkg.Open(",
			"db.Open(",
			"sql.Open(\"sqlite\"",
		} {
			if strings.Contains(src, forbidden) {
				t.Fatalf("%s contains forbidden persistent SQLite open %q", path, forbidden)
			}
		}
	}
}

// TestEphemeralCutoverDoesNotCreateProjectDBArtifacts is the primary evidence
// for the whole cutover: no .db, -wal, -shm, SQLite temp file or .index-offset
// is created anywhere by a full reindex.
//
// A sentinel like this fails in one specific way — by asserting that nothing was
// created because nothing ran — so it carries its own anti-vacuity checks and
// they are load-bearing, not decoration:
//
//   - isolateProjectDir. Without it this test reindexed TestMain's empty
//     process-wide sandbox, NOT its own fixture. withWorkingDir only chdirs, and
//     the working directory is priority 5 in paths.ResolveProjectDir — behind
//     WIPNOTE_PROJECT_DIR (2) and CLAUDE_PROJECT_DIR (3), both of which TestMain
//     sets. Measured: the test resolved to wipnote-test-project-*/.wipnote while
//     its fixture sat untouched. It then walked the fixture for artifacts, found
//     none — because nothing had ever run there — and passed. Verified blind:
//     with runReindex patched to write a real wipnote.db into the project it
//     resolved, this test still passed.
//   - the work-item and parse-failure assertions below. They are what stops it
//     silently reverting to walking an empty tree: a reindex that indexes zero
//     artifacts exercises almost none of the code this test polices.
func TestEphemeralCutoverDoesNotCreateProjectDBArtifacts(t *testing.T) {
	projectRoot := t.TempDir()
	isolateProjectDir(t, projectRoot)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(t.TempDir(), "override", "wipnote.db"))
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	makeMinimalProject(t, wipnoteDir)
	// A real repo, because a full reindex includes git-derived passes
	// (commit trailers, feature_files). Without one they fail with
	// "git log: exit status 128", which reindex counts as an artifact error —
	// so the fixture was reporting a parse failure it did not have, and those
	// passes never ran at all.
	initEphemeralFixtureRepo(t, projectRoot)

	database, err := openDB(wipnoteDir)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	database.Close()

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.Flags().Bool("full", true, "")
	cmd.Flags().BoolP("verbose", "v", false, "")
	withWorkingDir(t, projectRoot, func() {
		// Belt and braces on the isolation above: prove the command resolved to
		// THIS fixture before trusting anything it did.
		resolved, rErr := findWipnoteDir()
		if rErr != nil {
			t.Fatalf("findWipnoteDir: %v", rErr)
		}
		if resolveForCompare(resolved) != resolveForCompare(wipnoteDir) {
			t.Fatalf("reindex resolved to %q, not the fixture %q — the test would police a tree the code never touched",
				resolved, wipnoteDir)
		}
		if err := runReindex(cmd, nil); err != nil {
			t.Fatalf("runReindex: %v", err)
		}
	})

	// The reindex must have actually indexed the fixture. Zero work items means
	// it walked an empty tree and the artifact assertions below prove nothing.
	if got := parseReportedCount(t, out.String(), "work items"); got != 1 {
		t.Fatalf("reindex reported %d work items, want 1 — it did not index the fixture:\n%s", got, out.String())
	}
	if strings.Contains(out.String(), "failed to parse") {
		t.Fatalf("reindex could not parse the fixture; it is not exercising the real path:\n%s", out.String())
	}

	assertNoSQLiteArtifacts(t, projectRoot)
	assertNoSQLiteArtifacts(t, os.Getenv("XDG_CACHE_HOME"))
	assertNoSQLiteArtifacts(t, os.Getenv("XDG_DATA_HOME"))
	if _, err := os.Stat(os.Getenv("WIPNOTE_DB_PATH")); !os.IsNotExist(err) {
		t.Fatalf("WIPNOTE_DB_PATH artifact exists or stat failed unexpectedly: %v", err)
	}
}

// reportedCountRe matches a line of runReindex's per-table summary, e.g.
// "  work items       1".
var reportedCountRe = regexp.MustCompile(`(?m)^\s*(.+?)\s{2,}(\d+)\s*$`)

// parseReportedCount pulls one labelled count out of the reindex summary.
// Fails the test when the label is absent, so a change to the report shape
// surfaces as a failure rather than as a silently-zero count.
func parseReportedCount(t *testing.T, output, label string) int {
	t.Helper()
	for _, m := range reportedCountRe.FindAllStringSubmatch(output, -1) {
		if strings.TrimSpace(m[1]) != label {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("parse %q count %q: %v", label, m[2], err)
		}
		return n
	}
	t.Fatalf("reindex output has no %q line; the summary format changed:\n%s", label, output)
	return 0
}

func makeMinimalProject(t *testing.T, wipnoteDir string) {
	t.Helper()
	for _, dir := range []string{"features", "tracks", "bugs", "spikes", "plans", "sessions", "claims", "gates", "recaps"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// id=, not data-id=: htmlparse.parseDocument selects "article[id]" and
	// errors with "no <article id=...> found" otherwise. The original fixture
	// used data-id and was therefore unparseable — the second half of why this
	// test indexed nothing.
	const id = "feat-ab12cd34"
	feature := `<!DOCTYPE html><html><body><article id="` + id +
		`" data-type="feature" data-status="in-progress"><header><h1>Ephemeral Feature</h1></header></article></body></html>`
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", id+".html"), []byte(feature), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
}

// initFixtureRepo makes root a git repository with one commit, so reindex's
// git-derived passes execute instead of erroring out.
func initEphemeralFixtureRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if outBytes, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, outBytes, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("add", "--all")
	run("commit", "-q", "-m", "fixture")
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(wd) //nolint:errcheck
	fn()
}

func assertNoSQLiteArtifacts(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if name == "wipnote.db" || name == "htmlgraph.db" ||
			strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") ||
			strings.Contains(name, ".index-offset") {
			t.Fatalf("unexpected SQLite/cache artifact: %s", path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", root, err)
	}
}
