package guardprofile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProposedGuard wraps a Guard with discovery metadata so the approval UX can
// distinguish high-confidence signals (a declared CI/test script, a Makefile
// target) from low-confidence guesses. Approval is prune-not-invent: the human
// removes guards they do not want rather than inventing new ones, and the
// LowConfidence flag tells the UX which entries to surface for scrutiny.
type ProposedGuard struct {
	Guard         Guard
	Phase         string
	Source        string // human-readable provenance, e.g. "go.mod", "Makefile:test"
	LowConfidence bool   // true when the signal is a heuristic guess, not declared
}

// Proposal is the result of pure discovery: a phase-grouped Profile plus the
// per-guard provenance/confidence metadata. The Profile is ready to be
// approved-and-persisted by the launcher; Guards carries the rationale the UX
// renders. Proposal performs NO writes and NO git — it only inspects files
// under repoRoot.
type Proposal struct {
	Profile *Profile
	Guards  []ProposedGuard
}

// Propose inspects manifests, CI configs, Makefiles/scripts, and subprojects
// under repoRoot and returns a proposed phase-grouped guard Profile. It is pure:
// it reads files but never writes and never shells out to git. The returned
// Profile is unapproved (its Approved block is empty) — the caller owns TTY
// gating, approval, signing, and the commit.
//
// High-confidence signals (declared CI/test scripts, Makefile test targets) are
// emitted without the LowConfidence flag; heuristic guesses (a manifest implies
// a conventional command) are flagged so approval is prune-not-invent.
func Propose(repoRoot string, _ ...any) (*Proposal, error) {
	prop := &Proposal{Profile: &Profile{Guards: map[string][]Guard{}}}

	add := func(pg ProposedGuard) {
		prop.Guards = append(prop.Guards, pg)
		prop.Profile.Guards[pg.Phase] = append(prop.Profile.Guards[pg.Phase], pg.Guard)
	}

	// High-confidence: a Makefile with a `test` (or `check`) target is a
	// declared, project-author-curated command.
	if mk := readFile(repoRoot, "Makefile"); mk != "" {
		for _, target := range []string{"test", "check", "lint"} {
			if makeTargetExists(mk, target) {
				add(ProposedGuard{
					Phase:  PhaseQuality,
					Guard:  Guard{Name: "make-" + target, Cmd: "make " + target},
					Source: "Makefile:" + target,
				})
			}
		}
	}

	// Manifest-driven heuristics. These are conventional commands implied by a
	// manifest's presence — flagged low-confidence because the project may use a
	// different runner. Skipped when a Makefile already declared a target, to
	// avoid duplicate quality guards.
	hasMakeQuality := len(prop.Profile.Guards[PhaseQuality]) > 0

	if readFile(repoRoot, "go.mod") != "" {
		add(ProposedGuard{
			Phase:         PhaseQuality,
			Guard:         Guard{Name: "go-build", Cmd: "go build ./..."},
			Source:        "go.mod",
			LowConfidence: hasMakeQuality,
		})
		add(ProposedGuard{
			Phase:         PhaseQuality,
			Guard:         Guard{Name: "go-vet", Cmd: "go vet ./..."},
			Source:        "go.mod",
			LowConfidence: hasMakeQuality,
		})
		add(ProposedGuard{
			Phase:         PhaseCompletion,
			Guard:         Guard{Name: "go-test", Cmd: "go test ./..."},
			Source:        "go.mod",
			LowConfidence: hasMakeQuality,
		})
	}

	if pkg := readFile(repoRoot, "package.json"); pkg != "" {
		// A declared "test" script in package.json is high-confidence; a missing
		// one means `npm test` is a guess.
		hasTestScript := npmScriptExists(pkg, "test")
		add(ProposedGuard{
			Phase:         PhaseCompletion,
			Guard:         Guard{Name: "npm-test", Cmd: "npm test"},
			Source:        "package.json:scripts.test",
			LowConfidence: !hasTestScript,
		})
		if npmScriptExists(pkg, "lint") {
			add(ProposedGuard{
				Phase:  PhaseQuality,
				Guard:  Guard{Name: "npm-lint", Cmd: "npm run lint"},
				Source: "package.json:scripts.lint",
			})
		}
		if npmScriptExists(pkg, "build") {
			add(ProposedGuard{
				Phase:  PhaseQuality,
				Guard:  Guard{Name: "npm-build", Cmd: "npm run build"},
				Source: "package.json:scripts.build",
			})
		}
	}

	if readFile(repoRoot, "pyproject.toml") != "" {
		add(ProposedGuard{
			Phase:         PhaseCompletion,
			Guard:         Guard{Name: "pytest", Cmd: "uv run pytest"},
			Source:        "pyproject.toml",
			LowConfidence: true,
		})
		add(ProposedGuard{
			Phase:         PhaseQuality,
			Guard:         Guard{Name: "ruff", Cmd: "uv run ruff check ."},
			Source:        "pyproject.toml",
			LowConfidence: true,
		})
	}

	if readFile(repoRoot, "Cargo.toml") != "" {
		add(ProposedGuard{
			Phase:         PhaseQuality,
			Guard:         Guard{Name: "cargo-build", Cmd: "cargo build"},
			Source:        "Cargo.toml",
			LowConfidence: true,
		})
		add(ProposedGuard{
			Phase:         PhaseCompletion,
			Guard:         Guard{Name: "cargo-test", Cmd: "cargo test"},
			Source:        "Cargo.toml",
			LowConfidence: true,
		})
	}

	return prop, nil
}

// readFile returns the trimmed contents of repoRoot/rel, or "" when the file is
// absent or unreadable. Discovery treats unreadable files as absent.
func readFile(repoRoot, rel string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// makeTargetExists reports whether the Makefile text declares a `target:` rule.
func makeTargetExists(makefile, target string) bool {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `\s*:`)
	return re.MatchString(makefile)
}

// npmScriptExists reports whether package.json declares scripts.<name>. It is a
// lightweight string probe (not a full JSON parse) sufficient to flag
// confidence: it looks for "name" as a key inside a scripts block.
func npmScriptExists(pkgJSON, name string) bool {
	idx := strings.Index(pkgJSON, `"scripts"`)
	if idx < 0 {
		return false
	}
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:`)
	return re.MatchString(pkgJSON[idx:])
}
