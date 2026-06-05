package guardprofile

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFiles writes the given rel->content map under a fresh temp repo root.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// guardNames returns the set of guard names proposed for a phase.
func guardNames(p *Profile, phase string) map[string]string {
	out := map[string]string{}
	for _, g := range p.Guards[phase] {
		out[g.Name] = g.Cmd
	}
	return out
}

func TestPropose_FromManifests(t *testing.T) {
	tests := []struct {
		name       string
		files      map[string]string
		wantQualNm []string
		wantCompNm []string
	}{
		{
			name:       "go module",
			files:      map[string]string{"go.mod": "module x\n\ngo 1.22\n"},
			wantQualNm: []string{"go-build", "go-vet"},
			wantCompNm: []string{"go-test"},
		},
		{
			name: "node with scripts",
			files: map[string]string{
				"package.json": `{"scripts":{"test":"jest","lint":"eslint .","build":"tsc"}}`,
			},
			wantQualNm: []string{"npm-lint", "npm-build"},
			wantCompNm: []string{"npm-test"},
		},
		{
			name:       "rust",
			files:      map[string]string{"Cargo.toml": "[package]\nname = \"x\"\n"},
			wantQualNm: []string{"cargo-build"},
			wantCompNm: []string{"cargo-test"},
		},
		{
			name:       "python",
			files:      map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"},
			wantQualNm: []string{"ruff"},
			wantCompNm: []string{"pytest"},
		},
		{
			name:       "makefile test target is high confidence",
			files:      map[string]string{"Makefile": "test:\n\tgo test ./...\n"},
			wantQualNm: []string{"make-test"},
			wantCompNm: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFiles(t, tc.files)
			prop, err := Propose(root)
			if err != nil {
				t.Fatalf("Propose: %v", err)
			}
			if prop == nil || prop.Profile == nil {
				t.Fatal("nil proposal/profile")
			}
			qual := guardNames(prop.Profile, PhaseQuality)
			for _, name := range tc.wantQualNm {
				if _, ok := qual[name]; !ok {
					t.Errorf("missing quality guard %q; got %v", name, qual)
				}
			}
			comp := guardNames(prop.Profile, PhaseCompletion)
			for _, name := range tc.wantCompNm {
				if _, ok := comp[name]; !ok {
					t.Errorf("missing completion guard %q; got %v", name, comp)
				}
			}
			// Pure: Validate must accept the proposal.
			if err := Validate(prop.Profile); err != nil {
				t.Errorf("proposed profile failed Validate: %v", err)
			}
			// Pure: no approval recorded on a fresh proposal.
			if IsApproved(prop.Profile) {
				t.Error("fresh proposal must not be pre-approved")
			}
		})
	}
}

func TestPropose_FlagsLowConfidence(t *testing.T) {
	// package.json with NO test script: npm-test is a guess -> low confidence.
	root := writeFiles(t, map[string]string{
		"package.json": `{"name":"x","scripts":{"build":"tsc"}}`,
	})
	prop, err := Propose(root)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	var npmTest *ProposedGuard
	var npmBuild *ProposedGuard
	for i := range prop.Guards {
		switch prop.Guards[i].Guard.Name {
		case "npm-test":
			npmTest = &prop.Guards[i]
		case "npm-build":
			npmBuild = &prop.Guards[i]
		}
	}
	if npmTest == nil {
		t.Fatal("expected npm-test proposal")
	}
	if !npmTest.LowConfidence {
		t.Error("npm-test without declared test script must be flagged low-confidence")
	}
	if npmBuild == nil || npmBuild.LowConfidence {
		t.Error("npm-build from a declared build script must be high-confidence")
	}
}

func TestPropose_EmptyRepo(t *testing.T) {
	root := t.TempDir()
	prop, err := Propose(root)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(prop.Guards) != 0 {
		t.Errorf("empty repo should propose no guards, got %d", len(prop.Guards))
	}
}
