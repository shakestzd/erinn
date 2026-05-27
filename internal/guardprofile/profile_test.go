package guardprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, repoRoot, body string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".wipnote")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guard-profile.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	repo := t.TempDir()
	p, err := Load(repo)
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil profile for missing file, got %+v", p)
	}
}

func TestSignature_StableAcrossFormatting(t *testing.T) {
	repoA := t.TempDir()
	writeProfile(t, repoA, `
guards:
  quality:
    - name: build
      cmd: go build ./...
    - name: test
      cmd: go test ./...
`)
	// Same guards, comments added, keys reordered, extra whitespace, applies_when omitted vs empty.
	repoB := t.TempDir()
	writeProfile(t, repoB, `
# a comment
guards:
  quality:
    - cmd: go build ./...   # reordered keys
      name: build
    - cmd:  go test ./...
      name: test
`)
	a, err := Load(repoA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(repoB)
	if err != nil {
		t.Fatal(err)
	}
	if Signature(a) != Signature(b) {
		t.Fatalf("signatures differ across formatting:\n a=%s\n b=%s", Signature(a), Signature(b))
	}
}

func TestSignature_StableAcrossGuardReorder(t *testing.T) {
	repoA := t.TempDir()
	writeProfile(t, repoA, `
guards:
  quality:
    - name: build
      cmd: go build ./...
    - name: test
      cmd: go test ./...
`)
	repoB := t.TempDir()
	writeProfile(t, repoB, `
guards:
  quality:
    - name: test
      cmd: go test ./...
    - name: build
      cmd: go build ./...
`)
	a, _ := Load(repoA)
	b, _ := Load(repoB)
	if Signature(a) != Signature(b) {
		t.Fatalf("signatures differ across guard reorder:\n a=%s\n b=%s", Signature(a), Signature(b))
	}
}

func TestSignature_ChangesOnGuardEdit(t *testing.T) {
	base := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "build", Cmd: "go build ./...", Cwd: "x", AppliesWhen: &AppliesWhen{Paths: []string{"a/b.go"}}}},
	}}
	baseSig := Signature(base)

	mutators := map[string]func(p *Profile){
		"name":         func(p *Profile) { p.Guards[PhaseQuality][0].Name = "build2" },
		"cmd":          func(p *Profile) { p.Guards[PhaseQuality][0].Cmd = "go build -v ./..." },
		"cwd":          func(p *Profile) { p.Guards[PhaseQuality][0].Cwd = "y" },
		"applies_when": func(p *Profile) { p.Guards[PhaseQuality][0].AppliesWhen = &AppliesWhen{Paths: []string{"c/d.go"}} },
	}
	for field, mut := range mutators {
		clone := &Profile{Guards: map[string][]Guard{
			PhaseQuality: {{Name: "build", Cmd: "go build ./...", Cwd: "x", AppliesWhen: &AppliesWhen{Paths: []string{"a/b.go"}}}},
		}}
		mut(clone)
		if Signature(clone) == baseSig {
			t.Fatalf("editing %s did not change signature", field)
		}
	}
}

func TestIsApproved_FalseWhenContentDrifted(t *testing.T) {
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "build", Cmd: "go build ./..."}},
	}}
	// Approve at current content.
	p.Approved.Signature = Signature(p)
	if !IsApproved(p) {
		t.Fatalf("expected approved when signature matches content")
	}
	// Drift the content; approval must now be stale.
	p.Guards[PhaseQuality][0].Cmd = "rm -rf /"
	if IsApproved(p) {
		t.Fatalf("expected IsApproved=false after content drift")
	}
}

func TestIsApproved_FalseWhenNoSignature(t *testing.T) {
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "build", Cmd: "go build ./..."}},
	}}
	if IsApproved(p) {
		t.Fatalf("expected IsApproved=false when no approval recorded")
	}
}

func TestValidate_RejectsUnknownPhase(t *testing.T) {
	p := &Profile{Guards: map[string][]Guard{
		"bogus": {{Name: "x", Cmd: "echo"}},
	}}
	if err := Validate(p); err == nil {
		t.Fatalf("expected error for unknown phase")
	}
}

func TestValidate_RejectsMissingCmd(t *testing.T) {
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "noop"}},
	}}
	if err := Validate(p); err == nil {
		t.Fatalf("expected error for guard missing cmd")
	}
}

func TestValidate_NilAndValidPass(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Fatalf("nil profile should be valid, got %v", err)
	}
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality:    {{Name: "build", Cmd: "go build ./..."}},
		PhaseCompletion: {{Name: "test", Cmd: "go test ./..."}},
		PhaseYolo:       {{Name: "vet", Cmd: "go vet ./..."}},
	}}
	if err := Validate(p); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
}

func TestSignature_StableAcrossAppliesWhenPathOrderAndSlashes(t *testing.T) {
	a := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "g", Cmd: "c", AppliesWhen: &AppliesWhen{Paths: []string{"a/x.go", "b/y.go"}}}},
	}}
	b := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "g", Cmd: "c", AppliesWhen: &AppliesWhen{Paths: []string{"b\\y.go", "a/x.go"}}}},
	}}
	if Signature(a) != Signature(b) {
		t.Fatalf("applies_when path order/slash normalization not stable:\n a=%s\n b=%s", Signature(a), Signature(b))
	}
}

// TestIsApproved_FalseForInvalidProfile is the regression for roborev #3708: an
// invalid profile (e.g. a typo'd phase key like `completions:`) must never be
// approved, even if its recorded signature matches the canonical content —
// otherwise IsApproved says "approved" while the gates reject it and fall back.
func TestIsApproved_FalseForInvalidProfile(t *testing.T) {
	p := &Profile{Guards: map[string][]Guard{
		"completions": {{Name: "x", Cmd: "echo x"}}, // unknown phase (typo)
	}}
	p.Approved.Signature = Signature(p) // signature matches canonical content
	if IsApproved(p) {
		t.Error("invalid profile (unknown phase key) must never report approved")
	}
}
