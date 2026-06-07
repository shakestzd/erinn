package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/provenance"
)

// setupProvenanceFixture creates an empty .wipnote tree under a temp dir
// and returns (tmpDir, hgDir).
func setupProvenanceFixture(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs", "sessions"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return tmpDir, hgDir
}

// TestFeatureCreate_RecordsProvenanceFromFlags verifies that
// `wipnote feature create --created-by-model X --created-by-role Y` writes
// the provenance fields onto the feature HTML.
func TestFeatureCreate_RecordsProvenanceFromFlags(t *testing.T) {
	tmpDir, hgDir := setupProvenanceFixture(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Clean env so the env-var path is silent.
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("WIPNOTE_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	provenance.SetCLIVersion("dev") // keep predictable

	opts := &wiCreateOpts{
		priority:            "medium",
		description:         "test",
		standaloneReason:    "test fixture",
		createdByModel:      "claude-opus-4-7",
		createdByRole:       "architect-coder",
		createdByCLIVersion: "9.9.9",
	}
	if err := runWiCreate("feature", "Test Provenance Flags", opts); err != nil {
		t.Fatalf("runWiCreate: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(files) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(files))
	}
	node, err := htmlparse.ParseFile(files[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if node.CreatedByModel != "claude-opus-4-7" {
		t.Errorf("CreatedByModel = %q, want claude-opus-4-7", node.CreatedByModel)
	}
	if node.CreatedByRole != "architect-coder" {
		t.Errorf("CreatedByRole = %q, want architect-coder", node.CreatedByRole)
	}
	if node.CreatedByCLIVersion != "9.9.9" {
		t.Errorf("CreatedByCLIVersion = %q, want 9.9.9", node.CreatedByCLIVersion)
	}
}

// TestFeatureCreate_InheritsProvenanceFromActiveSession verifies that when an
// active session HTML carries provenance and no flags override it, the new
// feature inherits all four fields.
func TestFeatureCreate_InheritsProvenanceFromActiveSession(t *testing.T) {
	tmpDir, hgDir := setupProvenanceFixture(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	sessionID := "sess-prov-inherit-001"
	sessHTML := `<!DOCTYPE html>
<html lang="en"><head><title>session</title></head><body>
<article id="` + sessionID + `"
         data-type="session"
         data-status="active"
         data-agent="claude-code"
         data-created-by-agent="claude-code"
         data-created-by-model="claude-opus-4-7"
         data-created-by-role="architect-coder"
         data-created-by-cli-version="1.2.3">
</article></body></html>`
	if err := os.WriteFile(filepath.Join(hgDir, "sessions", sessionID+".html"), []byte(sessHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("WIPNOTE_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	provenance.SetCLIVersion("dev")

	opts := &wiCreateOpts{
		priority:         "medium",
		description:      "test",
		standaloneReason: "test fixture",
	}
	if err := runWiCreate("feature", "Inherit From Session", opts); err != nil {
		t.Fatalf("runWiCreate: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(files) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(files))
	}
	node, err := htmlparse.ParseFile(files[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if node.CreatedByAgent != "claude-code" {
		t.Errorf("CreatedByAgent = %q, want claude-code (inherited)", node.CreatedByAgent)
	}
	if node.CreatedByModel != "claude-opus-4-7" {
		t.Errorf("CreatedByModel = %q, want inherited", node.CreatedByModel)
	}
	if node.CreatedByRole != "architect-coder" {
		t.Errorf("CreatedByRole = %q, want inherited", node.CreatedByRole)
	}
	if node.CreatedByCLIVersion != "1.2.3" {
		t.Errorf("CreatedByCLIVersion = %q, want inherited 1.2.3", node.CreatedByCLIVersion)
	}
}

// TestShowRendersUnknownForLegacyItem verifies that an item without provenance
// shows "unknown" in human-readable output.
func TestShowRendersUnknownForLegacyItem(t *testing.T) {
	tmpDir, hgDir := setupProvenanceFixture(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	legacyID := "feat-legacy123"
	legacy := `<!DOCTYPE html>
<html lang="en"><head><title>Legacy</title></head><body>
<article id="` + legacyID + `"
         data-type="feature"
         data-status="todo"
         data-priority="medium">
  <header><h1>Legacy</h1></header>
</article></body></html>`
	if err := os.WriteFile(filepath.Join(hgDir, "features", legacyID+".html"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runWiShow(legacyID); err != nil {
			t.Fatalf("runWiShow: %v", err)
		}
	})
	if !strings.Contains(out, "Created by") {
		t.Errorf("show output missing 'Created by' line; got:\n%s", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("show output should report unknown for legacy item; got:\n%s", out)
	}
}
