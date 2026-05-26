package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/internal/guardprofile"
)

// TestHookGate_ResolvesProfile verifies the per-commit completion gate consults
// ResolveGuards: an approved yolo-phase guard profile drives the gate instead
// of manifest autodetection.
func TestHookGate_ResolvesProfile(t *testing.T) {
	dir := t.TempDir()
	// A go.mod is present so autodetection WOULD select "go test ./..." (which
	// fails on an empty module). The profile must take precedence.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Approved yolo profile whose single guard trivially passes.
	prof := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseYolo: {{Name: "noop", Cmd: "true"}},
	}}
	sig := guardprofile.Signature(prof)
	wdir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := "guards:\n  yolo:\n    - name: noop\n      cmd: \"true\"\n" +
		"approved:\n  signature: " + sig + "\n"
	if err := os.WriteFile(filepath.Join(wdir, "guard-profile.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	result := runTaskCompletionGate(dir)
	if !result.Passed {
		t.Fatalf("expected profile guard to pass, got fail: %s", result.Output)
	}
	if result.GateName != "guard-profile" {
		t.Errorf("expected guard-profile gate name, got %q (autodetection was used instead of the profile)", result.GateName)
	}
}

// TestHookGate_UnapprovedProfileFallsBack ensures an unapproved profile does
// NOT drive the gate (trust boundary): autodetection is used instead.
func TestHookGate_UnapprovedProfileFallsBack(t *testing.T) {
	dir := t.TempDir()
	wdir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No approved signature -> not honored.
	yaml := "guards:\n  yolo:\n    - name: noop\n      cmd: \"true\"\n"
	if err := os.WriteFile(filepath.Join(wdir, "guard-profile.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	// No manifest -> autodetection returns unknown-project-type pass.
	result := runTaskCompletionGate(dir)
	if result.GateName == "guard-profile" {
		t.Fatalf("unapproved profile must not drive the gate; got GateName=%q", result.GateName)
	}
}
