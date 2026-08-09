package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// provGit runs a git command in the test repo, failing the test on error.
func provGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// provInitRepo turns the test project into a real git repo with one commit, so
// the canonical provenance lookups (git log / git status / git diff-tree) have
// something to read. Everything already present is committed, which means the
// tree starts CLEAN — a precondition for the exempt cases.
func provInitRepo(t *testing.T, root string) {
	t.Helper()
	provGit(t, root, "init", "-q")
	provGit(t, root, "config", "user.email", "t@t")
	provGit(t, root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ignore build output. seedPassingGateRecord runs a real `go build` in this
	// fixture, which drops a binary named after the module in the project root;
	// left visible to git it would read as the item's uncommitted source and the
	// provenance gate would fire on the harness's own leftovers.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("/provenancegatetest\n*.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provGit(t, root, "add", "--all")
	provGit(t, root, "commit", "-q", "-m", "init")
}

// seedCodeBearing makes the item code-bearing with NO linked commit: it writes
// an uncommitted source file and records an implemented_in session edge on the
// artifact. Those are the two canonical facts the gate composes — "an agent
// worked this item" and "there is source outside .wipnote/ that is not in any
// commit referencing it" — and together they are the exact situation the gate
// exists to block.
func seedCodeBearing(t *testing.T, root, hgDir, itemID, filePath string) {
	t.Helper()
	abs := filepath.Join(root, filePath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedImplementedIn(t, hgDir, itemID)
}

// seedImplementedIn adds the canonical implemented_in edge recorded by
// `wipnote <type> start` when a session picks the item up.
func seedImplementedIn(t *testing.T, hgDir, itemID string) {
	t.Helper()
	path := itemArtifactPath(t, hgDir, itemID)
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if node.Edges == nil {
		node.Edges = map[string][]models.Edge{}
	}
	rel := string(models.RelImplementedIn)
	node.Edges[rel] = append(node.Edges[rel], models.Edge{
		TargetID:     "test-session-prov",
		Relationship: models.RelImplementedIn,
		Since:        time.Now().UTC(),
	})
	if _, err := workitem.WriteNodeHTML(filepath.Dir(path), node); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// itemArtifactPath locates an item's canonical artifact by ID.
func itemArtifactPath(t *testing.T, hgDir, itemID string) string {
	t.Helper()
	for _, dir := range []string{"features", "bugs", "spikes"} {
		p := filepath.Join(hgDir, dir, itemID+".html")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatalf("no artifact found for %s under %s", itemID, hgDir)
	return ""
}

// seedProvCommit commits the item's source file with the ID in the message,
// which is wipnote's canonical commit-linkage convention (the same one
// `wipnote reindex` parses). Returns the commit hash.
func seedProvCommit(t *testing.T, root, itemID, filePath string) string {
	t.Helper()
	abs := filepath.Join(root, filePath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	// The trailing marker guarantees the content differs from whatever the
	// fixture scaffolding already committed, so `git add` always stages
	// something and the commit is never empty.
	body := "package foo\n\n// linked: " + itemID + "\n"
	if filepath.Base(filePath) == "go.mod" {
		body = "module example.com/provenancegatetest\n\ngo 1.24\n\n// linked: " + itemID + "\n"
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	provGit(t, root, "add", "--", filePath)
	provGit(t, root, "commit", "-q", "-m", "impl ("+itemID+")")
	return provGit(t, root, "rev-parse", "HEAD")
}

// createItem creates a work item of the given type and returns its ID.
func createItem(t *testing.T, hgDir, typeName, title, trackID string) string {
	t.Helper()
	if err := testCreate(typeName, title, trackID, "medium", false, false); err != nil {
		t.Fatalf("create %s: %v", typeName, err)
	}
	dirName := typeName + "s"
	prefix := map[string]string{"feature": "feat-", "bug": "bug-", "spike": "spk-"}[typeName]
	files, _ := filepath.Glob(filepath.Join(hgDir, dirName, prefix+"*.html"))
	if len(files) == 0 {
		t.Fatalf("no %s file created", typeName)
	}
	node, _ := htmlparse.ParseFile(files[len(files)-1])
	return node.ID
}

func prepProject(t *testing.T) (tmpDir, hgDir string) {
	t.Helper()
	// test-session-prov — session-shaped id required by the canonical session ledger
	// that testHgDirWithDB now seeds.
	const sessionID = "019ee378-abcd-7000-8000-000000000302"
	tmpDir, hgDir = testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })
	// projectDirFlag alone is enough for findWipnoteDir (it is ExplicitDir,
	// priority 1), but hooks.ResolveProjectDir passes no ExplicitDir and would
	// fall through to WIPNOTE_PROJECT_DIR / CLAUDE_PROJECT_DIR — which are set
	// in a live agent session and point at the REAL repo. Pin both.
	isolateProjectDir(t, tmpDir)
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)
	// Git is the canonical provenance store, so the fixture has to be a repo.
	// It starts CLEAN: anything a test wants counted as uncommitted has to be
	// written deliberately.
	provInitRepo(t, tmpDir)
	return tmpDir, hgDir
}

func seedPassingGateRecord(t *testing.T, projectRoot, sessionID, workItemID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectRoot, "plugin", "config"), 0o755); err != nil {
		t.Fatalf("mkdir gate config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module example.com/provenancegatetest\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "plugin", "config", "quality-gate-flake-allowlist.json"), []byte(`[
  {
    "id": "tmp-noexec",
    "match_all": ["/tmp/", "permission denied"],
    "justification": "Test fixture justification"
  },
  {
    "id": "listener-socket-sandbox",
    "match_all": ["listen tcp", "socket: operation not permitted"],
    "justification": "Test fixture listener sandbox justification"
  }
]`), 0o644); err != nil {
		t.Fatalf("write gate allowlist: %v", err)
	}

	tmpBase := execCapableBase(t)
	for _, dir := range []string{"gotmp-exec", "gocache"} {
		if err := os.MkdirAll(filepath.Join(tmpBase, dir), 0o755); err != nil {
			t.Fatalf("mkdir external %s: %v", dir, err)
		}
	}
	t.Setenv("TMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOTMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOCACHE", filepath.Join(tmpBase, "gocache"))

	// Commit the scaffolding this helper just wrote. It is fixture, not the
	// item's implementation: left uncommitted it would read as "uncommitted
	// source" to the provenance gate and as a dependency-manifest change
	// (go.mod) to the research gate, and both would fire on scaffolding.
	provGit(t, projectRoot, "add", "--", "go.mod", "main.go", "plugin")
	provGit(t, projectRoot, "commit", "-q", "-m", "test scaffolding")

	result, err := runSessionGate(projectRoot, sessionID, workItemID, "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	if result == nil || !result.Passed || result.Record == nil || !result.Record.SignatureValid() {
		t.Fatalf("expected valid passing gate record, got %+v", result)
	}
}

// 1. Code-bearing feature, zero commits, no flag → blocked non-zero.
func TestProvenanceGate_CodeBearingFeatureZeroCommits_Blocked(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Code Feature", trackID)
	seedCodeBearing(t, tmpDir, hgDir, id, "internal/foo/bar.go")

	wiAcceptedAdvisory = ""
	err := runWiSetStatus("feature", id, "done")
	if err == nil {
		t.Fatalf("expected completion to be blocked for zero-commit code-bearing feature")
	}
	if !strings.Contains(err.Error(), "accepted-advisory") {
		t.Errorf("error should mention --accepted-advisory remediation, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status == models.StatusDone {
		t.Errorf("item must not be marked done when gate blocks")
	}
}

// 2. Code-bearing BUG and SPIKE with zero commits → also blocked (type-agnostic).
func TestProvenanceGate_CodeBearingBugAndSpikeZeroCommits_Blocked(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	for _, tc := range []struct{ typeName, dir string }{
		{"bug", "bugs"},
		{"spike", "spikes"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			tmpDir, hgDir := prepProject(t)
			trackID := testSetupTrack(t, hgDir)
			id := createItem(t, hgDir, tc.typeName, "Code "+tc.typeName, trackID)
			seedCodeBearing(t, tmpDir, hgDir, id, "cmd/wipnote/thing.go")

			wiAcceptedAdvisory = ""
			err := runWiSetStatus(tc.typeName, id, "done")
			if err == nil {
				t.Fatalf("expected %s completion blocked (type-agnostic provenance gate)", tc.typeName)
			}
			node, _ := htmlparse.ParseFile(filepath.Join(hgDir, tc.dir, id+".html"))
			if node.Status == models.StatusDone {
				t.Errorf("%s must not be done when gate blocks", tc.typeName)
			}
		})
	}
}

// 3. Same with --accepted-advisory → completes, reason persisted + in check.
func TestProvenanceGate_AdvisoryOverrideCompletesAndPersists(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Advisory Feature", trackID)
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)
	seedCodeBearing(t, tmpDir, hgDir, id, "internal/foo/bar.go")

	const reason = "infra-only refactor, no source commit by design"
	wiAcceptedAdvisory = reason
	t.Cleanup(func() { wiAcceptedAdvisory = "" })

	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("expected completion to succeed with --accepted-advisory, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item should be done after advisory override, status=%s", node.Status)
	}
	got := acceptedAdvisoryOf(node)
	if got != reason {
		t.Errorf("accepted_advisory not persisted on artifact: got %q want %q", got, reason)
	}

	// Surfaced by `wipnote check accepted-advisory` (compliance output).
	var sb strings.Builder
	if err := runCheckAcceptedAdvisory(&sb); err != nil {
		t.Fatalf("runCheckAcceptedAdvisory: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, id) || !strings.Contains(out, reason) {
		t.Errorf("check accepted-advisory output missing id/reason:\n%s", out)
	}
}

// 4. Pure-.wipnote/doc item, zero commits → completes normally (exempt).
func TestProvenanceGate_PureWipnoteItemExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Docs Feature", trackID)
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)
	// Implemented by a session, but the only thing it touched is its own
	// .wipnote artifact — no source path anywhere, committed or not.
	seedImplementedIn(t, hgDir, id)

	wiAcceptedAdvisory = ""
	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("pure-.wipnote item should complete normally, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("exempt item should be done, status=%s", node.Status)
	}
}

// 5. Item with >=1 commit row → completes normally.
func TestProvenanceGate_HasCommitsCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Committed Feature", trackID)
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)
	seedImplementedIn(t, hgDir, id)
	seedProvCommit(t, tmpDir, id, "internal/foo/bar.go")

	wiAcceptedAdvisory = ""
	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("item with linked commit should complete, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item with commit should be done, status=%s", node.Status)
	}
}

// canonicalCodeBearingPaths unit: the item's own .wipnote artifact never counts
// as source, and a committed source path does.
func TestCanonicalCodeBearingPaths_ExcludesWipnote(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Path Classification", trackID)

	// Commit the item's artifact AND a source file under the same work-item ID.
	provGit(t, tmpDir, "add", "--", ".wipnote")
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "db", "lineage_repo.go"),
		[]byte("package db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provGit(t, tmpDir, "add", "--", "internal/db/lineage_repo.go")
	provGit(t, tmpDir, "commit", "-q", "-m", "impl ("+id+")")

	commits := canonicalLinkedCommits(tmpDir, id, nil)
	if len(commits) != 1 {
		t.Fatalf("expected exactly one linked commit for %s, got %v", id, commits)
	}
	paths := canonicalCodeBearingPaths(tmpDir, hgDir, id, nil, commits)
	if len(paths) != 1 || paths[0] != "internal/db/lineage_repo.go" {
		t.Errorf("expected only the non-.wipnote source path, got %v", paths)
	}

	// An item nothing references and nothing implemented is not code-bearing.
	const unknown = "feat-nonexistent"
	if got := canonicalCodeBearingPaths(tmpDir, hgDir, unknown, nil,
		canonicalLinkedCommits(tmpDir, unknown, nil)); len(got) != 0 {
		t.Errorf("expected no code-bearing paths for unknown item, got %v", got)
	}
}

// TestCanonicalLinkedCommits_IgnoresIncidentalMention pins that linkage follows
// wipnote's commit convention rather than a bare substring match: a commit that
// merely names the ID in prose is not provenance for it.
func TestCanonicalLinkedCommits_IgnoresIncidentalMention(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Mention Only", trackID)

	if err := os.WriteFile(filepath.Join(tmpDir, "note.txt"),
		[]byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provGit(t, tmpDir, "add", "--", "note.txt")
	provGit(t, tmpDir, "commit", "-q", "-m", "docs: mentions "+id+" in passing")

	if got := canonicalLinkedCommits(tmpDir, id, nil); len(got) != 0 {
		t.Errorf("an incidental mention must not count as linkage, got %v", got)
	}

	// The parenthesised convention does link.
	provGit(t, tmpDir, "commit", "-q", "--allow-empty", "-m", "fix: real work ("+id+")")
	if got := canonicalLinkedCommits(tmpDir, id, nil); len(got) != 1 {
		t.Errorf("expected the parenthesised convention to link, got %v", got)
	}

	// So does an explicit Refs: trailer.
	provGit(t, tmpDir, "commit", "-q", "--allow-empty", "-m", "chore: more\n\nRefs: "+id)
	if got := canonicalLinkedCommits(tmpDir, id, nil); len(got) != 2 {
		t.Errorf("expected the Refs: trailer to link, got %v", got)
	}
}

// TestGateRecordGuard_AdvisoryBypassesGateRecord verifies Fix 2: when
// --accepted-advisory is set, the gate-record guard (guard 4) is skipped so
// `feature/bug/spike complete --allow-dirty --accepted-advisory "<reason>"` can
// succeed even when no passing gate record exists in the DB.
func TestGateRecordGuard_AdvisoryBypassesGateRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("drives completion gate bypass flow")
	}

	for _, tc := range []string{"feature", "bug", "spike"} {
		t.Run(tc, func(t *testing.T) {
			tmpDir, hgDir := prepProject(t)
			trackID := testSetupTrack(t, hgDir)
			id := createItem(t, hgDir, tc, "Advisory Bypass "+tc, trackID)
			seedImplementedIn(t, hgDir, id)
			// A linked commit makes the item code-bearing (so the gate-record
			// guard has something to fire on) AND satisfies the provenance gate.
			seedProvCommit(t, tmpDir, id, "internal/foo/bar.go")

			// Do NOT call seedPassingGateRecord — gate-record guard must be
			// bypassed by --accepted-advisory alone.
			const reason = "no gate record available; accepted-advisory bypasses guard 4"
			wiAcceptedAdvisory = reason
			wiAllowDirtyComplete = true
			t.Cleanup(func() {
				wiAcceptedAdvisory = ""
				wiAllowDirtyComplete = false
			})

			if err := runWiSetStatus(tc, id, "done"); err != nil {
				t.Fatalf("expected complete to succeed with --accepted-advisory, got: %v", err)
			}
			dirName := tc + "s"
			node, _ := htmlparse.ParseFile(filepath.Join(hgDir, dirName, id+".html"))
			if node.Status != models.StatusDone {
				t.Errorf("%s should be done after advisory bypass, status=%s", tc, node.Status)
			}
		})
	}
}

// TestProvenanceGate_ManuallyLinkedCommitUnblocksCompletion is the regression
// guard for the hole a message-only gate leaves: `wipnote link-commit` exists
// precisely for commits that do NOT name their work item, so the commit here is
// deliberately anonymous. A test whose commit message named the item would pass
// through the message-derived path and prove nothing about the manual link.
func TestProvenanceGate_ManuallyLinkedCommitUnblocksCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Manually Linked Feature", trackID)
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)
	seedImplementedIn(t, hgDir, id)

	// Source work, committed WITHOUT the work-item ID anywhere in the message.
	srcRel := "internal/foo/bar.go"
	abs := filepath.Join(tmpDir, srcRel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provGit(t, tmpDir, "add", "--", srcRel)
	provGit(t, tmpDir, "commit", "-q", "-m", "refactor: tidy the foo package")
	sha := provGit(t, tmpDir, "rev-parse", "HEAD")

	// A second, still-uncommitted source file. This is what keeps the test
	// NON-VACUOUS, and it is not decoration: without it the item has no
	// code-bearing evidence at all once the work is committed anonymously, so
	// the gate exempts it and completion succeeds whether or not the link is
	// honoured. (Verified: the first draft of this test passed with the
	// committed_in union compiled out.) Untracked, so the earlier --allow-dirty
	// gate — which only sees dirty TRACKED files — stays out of the way.
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "foo", "extra.go"),
		[]byte("package foo\n\nvar Extra = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Precondition: the message-derived half genuinely cannot see the commit.
	if got := canonicalLinkedCommits(tmpDir, id, nil); len(got) != 0 {
		t.Fatalf("precondition: an anonymous commit must not be message-linked, got %v", got)
	}

	// The manual link: the same committed_in edge `wipnote link-commit` writes.
	linkCommitEdge(t, hgDir, id, sha, "refactor: tidy the foo package")

	// Precondition: with the link in place the item now HAS provenance, and it
	// is code-bearing either way — so the completion below turns purely on
	// whether the gate honours the edge.
	linked, err := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if err != nil {
		t.Fatalf("parse artifact: %v", err)
	}
	if got := canonicalLinkedCommits(tmpDir, id, linked); len(got) != 1 || got[0] != sha {
		t.Fatalf("committed_in edge not honoured as provenance: got %v, want [%s]", got, sha)
	}
	if got := canonicalCodeBearingPaths(tmpDir, hgDir, id, linked, nil); len(got) == 0 {
		t.Fatal("precondition: the item must be code-bearing, or the gate exempts it and proves nothing")
	}

	wiAcceptedAdvisory = ""
	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("a manually linked commit must satisfy the provenance gate, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item should be done after link-commit supplied provenance, status=%s", node.Status)
	}
}

// linkCommitEdge writes the committed_in edge in the exact shape link_commit.go
// produces: full 40-char SHA target, commit subject as title, author timestamp.
func linkCommitEdge(t *testing.T, hgDir, itemID, sha, subject string) {
	t.Helper()
	path := itemArtifactPath(t, hgDir, itemID)
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if node.Edges == nil {
		node.Edges = map[string][]models.Edge{}
	}
	rel := string(RelCommittedIn)
	node.Edges[rel] = append(node.Edges[rel], models.Edge{
		TargetID:     sha,
		Relationship: RelCommittedIn,
		Title:        subject,
		Since:        time.Now().UTC(),
	})
	if _, err := workitem.WriteNodeHTML(filepath.Dir(path), node); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCanonicalCodeBearingPaths_UncommittedOnlyWhenImplemented pins the scoping
// rule that keeps the gate from firing on unrelated working-tree noise: an
// uncommitted source file counts as the item's implementation only when
// canonical state says an agent actually worked the item.
func TestCanonicalCodeBearingPaths_UncommittedOnlyWhenImplemented(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Uncommitted Scope", trackID)

	// Uncommitted source exists in the tree, but nothing records that this item
	// was implemented → not attributable to it.
	if err := os.MkdirAll(filepath.Join(tmpDir, "internal", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "internal", "db", "lineage_repo.go"),
		[]byte("package db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := canonicalCodeBearingPaths(tmpDir, hgDir, id, nil, nil); len(got) != 0 {
		t.Errorf("uncommitted source must not attach to an unimplemented item, got %v", got)
	}

	// Once the implemented_in edge is recorded, the same file is the item's
	// uncommitted implementation.
	seedImplementedIn(t, hgDir, id)
	node, err := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if err != nil {
		t.Fatalf("parse artifact: %v", err)
	}
	got := canonicalCodeBearingPaths(tmpDir, hgDir, id, node, nil)
	if len(got) != 1 || got[0] != "internal/db/lineage_repo.go" {
		t.Errorf("expected the uncommitted source path (and nothing from .wipnote/), got %v", got)
	}
}
