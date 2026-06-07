package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/guardprofile"
)

// writeApprovedProfile writes an approved guard profile under projectRoot and
// returns its canonical signature.
func writeApprovedProfile(t *testing.T, projectRoot, cmd string) string {
	t.Helper()
	prof := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality: {{Name: "g", Cmd: cmd}},
	}}
	sig := guardprofile.Signature(prof)
	dir := filepath.Join(projectRoot, ".wipnote")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := "guards:\n  quality:\n    - name: g\n      cmd: " + cmd + "\napproved:\n  signature: " + sig + "\n"
	if err := os.WriteFile(filepath.Join(dir, "guard-profile.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return sig
}

// TestCompletion_StaleWhenSigDrifts verifies a recorded profile_signature that
// differs from the current approved signature is REPORTED (read-only) and not
// treated as a failure.
func TestCompletion_StaleWhenSigDrifts(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	// Current approved profile (signature A).
	currentSig := writeApprovedProfile(t, projectRoot, "true")

	// A prior passing gate record recorded under a DIFFERENT (stale) signature.
	rec := &dbpkg.GateRecord{
		SessionID:        "sess-drift",
		ProjectType:      "go",
		GateCommand:      "true",
		Status:           "pass",
		Source:           "check",
		ProfileSignature: "sha256:staleoldsignature",
		GuardsRunJSON:    `["g"]`,
	}
	if err := dbpkg.InsertGateRecord(database, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var buf bytes.Buffer
	reportGuardProfileDrift(database, projectRoot, "sess-drift", &buf)
	out := buf.String()
	if !strings.Contains(out, "drift") {
		t.Fatalf("expected drift notice, got %q", out)
	}
	if !strings.Contains(out, currentSig) {
		t.Errorf("expected notice to mention current signature %s, got %q", currentSig, out)
	}
}

// TestCompletion_NoDriftWhenSigMatches verifies no notice is printed when the
// recorded signature matches the approved profile.
func TestCompletion_NoDriftWhenSigMatches(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	currentSig := writeApprovedProfile(t, projectRoot, "true")
	rec := &dbpkg.GateRecord{
		SessionID:        "sess-match",
		ProjectType:      "go",
		GateCommand:      "true",
		Status:           "pass",
		Source:           "check",
		ProfileSignature: currentSig,
		GuardsRunJSON:    `["g"]`,
	}
	if err := dbpkg.InsertGateRecord(database, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var buf bytes.Buffer
	reportGuardProfileDrift(database, projectRoot, "sess-match", &buf)
	if buf.Len() != 0 {
		t.Fatalf("expected no drift notice, got %q", buf.String())
	}
}
