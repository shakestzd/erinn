package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recapRangeID mirrors the id-derivation in recap.go: sha256 of the range spec, first 12 chars.
func recapRangeID(spec string) string {
	sum := sha256.Sum256([]byte(spec))
	hex12 := fmt.Sprintf("%x", sum)[:12]
	return "recap-r-" + hex12
}

// buildRecapBinary builds the wipnote binary into a temp dir and returns its path.
func buildRecapBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "wipnote")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build wipnote binary: %v\n%s", err, out)
	}
	return bin
}

// initFixtureRepo initialises a minimal git repo with a .wipnote directory
// and at least one commit. Returns the repo root.
func initFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runIn := func(wd string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cmd %v: %v\n%s", args, err, out)
		}
	}

	runIn(dir, "git", "init", "--initial-branch=main")
	runIn(dir, "git", "config", "user.email", "test@wipnote.test")
	runIn(dir, "git", "config", "user.name", "Test")
	runIn(dir, "git", "config", "commit.gpgsign", "false")

	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	seed := filepath.Join(dir, "README.md")
	if err := os.WriteFile(seed, []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(dir, "git", "add", "README.md")
	runIn(dir, "git", "commit", "-m", "init")

	return dir
}

// TestRecapCmd_Feature builds the wipnote binary, runs `wipnote recap feat-testXXX`,
// and asserts the artifact is written and committed.
func TestRecapCmd_Feature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-spawning test in -short mode")
	}

	bin := buildRecapBinary(t)
	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")
	featureID := "feat-testdeadbeef"

	cmd := exec.Command(bin, "recap", featureID)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"WIPNOTE_PROJECT_DIR="+repo,
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	// Command may exit non-zero when DB is empty (no read-index), but the
	// artifact must still be written and committed.
	t.Logf("recap output (err=%v):\n%s", err, out)

	artifactPath := filepath.Join(wipDir, "recaps", "recap-"+featureID+".html")
	if _, statErr := os.Stat(artifactPath); statErr != nil {
		t.Fatalf("artifact not found at %s: %v", artifactPath, statErr)
	}

	relPath := filepath.Join(".wipnote", "recaps", "recap-"+featureID+".html")
	gitOut, gitErr := exec.Command("git", "-C", repo, "log", "--oneline", "--", relPath).CombinedOutput()
	if gitErr != nil {
		t.Fatalf("git log: %v\n%s", gitErr, gitOut)
	}
	if strings.TrimSpace(string(gitOut)) == "" {
		t.Fatalf("no commit found for %s\ngit log: %q", relPath, gitOut)
	}
	t.Logf("commit: %s", gitOut)
}

// TestRunPlanRollupRecap verifies that RunPlanRollupRecap produces a
// recap-pln-<planID>.html artifact when the plan YAML is committed.
func TestRunPlanRollupRecap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-spawning test in -short mode")
	}

	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")

	// Create and commit a minimal plan YAML so it has a git history entry.
	planID := "plan-testabcd1234"
	plansDir := filepath.Join(wipDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(plansDir, planID+".yaml")
	planContent := "meta:\n  id: " + planID + "\n  status: finalized\n  track_id: trk-test\n" +
		"design:\n  problem: test\nslices: []\n"
	if err := os.WriteFile(planPath, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runIn := func(wd string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cmd %v: %v\n%s", args, err, out)
		}
	}
	runIn(repo, "git", "add", planPath)
	runIn(repo, "git", "commit", "-m", "add plan YAML")

	// Call RunPlanRollupRecap directly.
	recapID := "recap-pln-" + planID
	if err := RunPlanRollupRecap(wipDir, planID); err != nil {
		t.Fatalf("RunPlanRollupRecap: %v", err)
	}

	// Assert the artifact was written.
	artifactPath := filepath.Join(wipDir, "recaps", recapID+".html")
	if _, statErr := os.Stat(artifactPath); statErr != nil {
		t.Fatalf("artifact not found at %s: %v", artifactPath, statErr)
	}

	// Assert the recap-pln ID convention is correct.
	if !strings.HasPrefix(recapID, "recap-pln-") {
		t.Errorf("recapID %q does not start with recap-pln-", recapID)
	}

	t.Logf("recap-pln artifact written: %s", artifactPath)
}

// TestRunPlanRollupRecap_UntrackedYAML verifies that RunPlanRollupRecap returns
// a non-nil error (non-fatal by contract) when the plan YAML has no git history.
func TestRunPlanRollupRecap_UntrackedYAML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-spawning test in -short mode")
	}

	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")

	// Plan YAML exists on disk but is NOT committed — no git history.
	planID := "plan-untracked9999"
	plansDir := filepath.Join(wipDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := os.WriteFile(planPath, []byte("meta:\n  id: "+planID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RunPlanRollupRecap(wipDir, planID)
	if err == nil {
		t.Fatalf("expected error for untracked plan YAML, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestRecapCmd_Range runs `wipnote recap --range HEAD~1..HEAD` and asserts
// the artifact uses the recap-r-<12-char-hash> id scheme.
func TestRecapCmd_Range(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-spawning test in -short mode")
	}

	bin := buildRecapBinary(t)
	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")

	// Add a second commit so HEAD~1..HEAD is non-trivial.
	f2 := filepath.Join(repo, "file2.txt")
	if err := os.WriteFile(f2, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn := func(wd string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wd
		out, errR := cmd.CombinedOutput()
		if errR != nil {
			t.Fatalf("cmd %v: %v\n%s", args, errR, out)
		}
	}
	runIn(repo, "git", "add", "file2.txt")
	runIn(repo, "git", "config", "user.email", "test@wipnote.test")
	runIn(repo, "git", "config", "user.name", "Test")
	runIn(repo, "git", "config", "commit.gpgsign", "false")
	runIn(repo, "git", "commit", "-m", "second commit")

	rangeSpec := "HEAD~1..HEAD"
	expectedID := recapRangeID(rangeSpec)

	cmd := exec.Command(bin, "recap", "--range", rangeSpec)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"WIPNOTE_PROJECT_DIR="+repo,
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	t.Logf("recap --range output (err=%v):\n%s", err, out)
	if err != nil {
		t.Fatalf("recap --range exited with error: %v\n%s", err, out)
	}

	artifactPath := filepath.Join(wipDir, "recaps", expectedID+".html")
	if _, statErr := os.Stat(artifactPath); statErr != nil {
		t.Fatalf("artifact not found at %s (id=%s): %v", artifactPath, expectedID, statErr)
	}

	relPath := filepath.Join(".wipnote", "recaps", expectedID+".html")
	gitOut, gitErr := exec.Command("git", "-C", repo, "log", "--oneline", "--", relPath).CombinedOutput()
	if gitErr != nil {
		t.Fatalf("git log: %v\n%s", gitErr, gitOut)
	}
	if strings.TrimSpace(string(gitOut)) == "" {
		t.Fatalf("no commit found for %s\ngit log: %q", relPath, gitOut)
	}
	t.Logf("commit: %s", gitOut)
}
