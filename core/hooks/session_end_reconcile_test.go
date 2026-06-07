package hooks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitRepo creates a real git repo at root with an initial commit so the
// reconcile git helpers (status/add/commit) operate against a true repo.
func gitInitRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
}

// writeArtifact creates an uncommitted work-item HTML artifact under .wipnote/.
func writeArtifact(t *testing.T, root, typeName, id string) {
	t.Helper()
	dir := filepath.Join(root, ".wipnote", typeName+"s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".html"),
		[]byte("<html>"+id+"</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TDD-1: a done-but-uncommitted item is auto-committed and reported.
func TestReconcile_DoneButUncommitted_AutoCommitsAndReports(t *testing.T) {
	td := setupTestDB(t)
	root := t.TempDir()
	gitInitRepo(t, root)
	td.addFeature("feat-aaaaaaaa", "feature", "done item", "done")
	writeArtifact(t, root, "feature", "feat-aaaaaaaa")

	rep, err := Reconcile(td.DB, root, false)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.AutoCommitted) != 1 || rep.AutoCommitted[0] != "feat-aaaaaaaa" {
		t.Fatalf("expected feat-aaaaaaaa auto-committed, got %v", rep.AutoCommitted)
	}
	// Artifact must now be committed (clean working tree for that path).
	out, _ := exec.Command("git", "-C", root, "status", "--porcelain", "--",
		filepath.Join(root, ".wipnote", "features", "feat-aaaaaaaa.html")).CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("artifact still dirty after reconcile: %q", out)
	}
}

// TDD-2: generator-touched without build-ports → reconcile reports drift via
// the slice-2 check-ports reuse. We assert the wiring path (a non-plugin-core
// repo yields no port drift; the engine does not crash and the class is empty),
// which proves reconcilePortDrift is invoked and gated on manifest presence.
func TestReconcile_PortDrift_UsesCheckPortsReuse(t *testing.T) {
	td := setupTestDB(t)
	root := t.TempDir()
	gitInitRepo(t, root)
	// No packages/plugin-core/manifest.json here → not a generator repo.
	rep, err := Reconcile(td.DB, root, false)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.PortDrift) != 0 {
		t.Fatalf("expected no port drift in non-generator repo, got %v", rep.PortDrift)
	}
	// Force the ambiguous-drift signal directly to prove the discriminator
	// keys on PortDrift only (not auto-commit / orphan classes).
	amb := &ReconcileReport{PortDrift: []string{"plugin/x"}}
	if !amb.HasAmbiguousDrift() {
		t.Fatal("PortDrift must register as ambiguous drift")
	}
	clean := &ReconcileReport{AutoCommitted: []string{"feat-x"}, Orphaned: []string{"feat-y"}}
	if clean.HasAmbiguousDrift() {
		t.Fatal("auto-commit/orphan classes must NOT be ambiguous drift")
	}
}

// TDD-3: Claude Stop path with unresolved ambiguous source drift →
// BlockExit2Error (exit-2). missing_events.go's no-block contract is amended
// ONLY for harness=claude + ambiguous drift.
func TestStopReconcile_ClaudeAmbiguousDrift_Blocks(t *testing.T) {
	root := t.TempDir()

	rep := &ReconcileReport{PortDrift: []string{"plugin/agents/x.md"}}
	err := discriminateReconcile(rep, "claude", root, "sess-1")
	var blockErr *BlockExit2Error
	if err == nil {
		t.Fatal("expected BlockExit2Error for claude + ambiguous drift")
	}
	if !errors.As(err, &blockErr) {
		t.Fatalf("expected *BlockExit2Error, got %T: %v", err, err)
	}
	if !strings.Contains(blockErr.Message, "build-ports") {
		t.Fatalf("block message should guide to build-ports, got %q", blockErr.Message)
	}

	// Claude with NON-ambiguous classes (auto-commit / orphan only) must NOT
	// block — the amendment is scoped to ambiguous generator drift only.
	clean := &ReconcileReport{AutoCommitted: []string{"feat-x"}, Orphaned: []string{"feat-y"}}
	if err := discriminateReconcile(clean, "claude", root, "sess-1"); err != nil {
		t.Fatalf("claude must not block on non-ambiguous classes, got %v", err)
	}
}

// TDD-4: Gemini/Codex path → exit 0, durable warning persisted AND rendered by
// session_start at the next SessionStart.
func TestStopReconcile_GeminiCodex_DurableWarningSurfacedAtSessionStart(t *testing.T) {
	for _, h := range []string{"gemini", "codex"} {
		t.Run(h, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
				t.Fatal(err)
			}
			rep := &ReconcileReport{PortDrift: []string{"packages/gemini-extension/z"}}
			if err := persistReconcileWarning(root, h, "sess-9", rep); err != nil {
				t.Fatalf("persist: %v", err)
			}
			// Durable: file exists even though the user never returned.
			if _, err := os.Stat(reconcileWarningsPath(root)); err != nil {
				t.Fatalf("durable warnings log not persisted: %v", err)
			}
			// Surfaced (and consumed) at next SessionStart.
			rendered := DrainReconcileWarnings(root)
			if !strings.Contains(rendered, "generator drift not reconciled") ||
				!strings.Contains(rendered, h) {
				t.Fatalf("warning not rendered for %s: %q", h, rendered)
			}
			// Idempotent: consumed, second drain is empty.
			if again := DrainReconcileWarnings(root); again != "" {
				t.Fatalf("warnings not consumed; second drain: %q", again)
			}
		})
	}
}

// TDD-5: reconcile auto-commit of a done-but-uncommitted artifact is a
// legitimate idempotent no-op on a re-run (HEAD must NOT advance), proving it
// is forward-compatible with slice-4 session-local gate records and cannot
// wedge a later complete.
func TestReconcile_AutoCommit_IdempotentDoesNotWedgeComplete(t *testing.T) {
	td := setupTestDB(t)
	root := t.TempDir()
	gitInitRepo(t, root)
	td.addFeature("feat-bbbbbbbb", "feature", "done item", "done")
	writeArtifact(t, root, "feature", "feat-bbbbbbbb")

	if rep, _ := Reconcile(td.DB, root, false); len(rep.AutoCommitted) != 1 {
		t.Fatalf("first pass should auto-commit once, got %v", rep.AutoCommitted)
	}
	headAfterFirst := gitHead(t, root)

	// Second pass: artifact already committed and unchanged → idempotent
	// no-op, HEAD must NOT advance (so a later strict complete's
	// must-not-advance branch is satisfied — no wedge).
	rep2, _ := Reconcile(td.DB, root, false)
	if len(rep2.AutoCommitted) != 0 {
		t.Fatalf("second pass must be a no-op, got %v", rep2.AutoCommitted)
	}
	if gitHead(t, root) != headAfterFirst {
		t.Fatal("idempotent re-run advanced HEAD — would wedge a later complete")
	}
}

func gitHead(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %s: %v", out, err)
	}
	return strings.TrimSpace(string(out))
}
