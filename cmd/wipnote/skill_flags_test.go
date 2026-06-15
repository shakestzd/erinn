package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSkillFlagsIntegration scans the plugin asset tree for `wipnote ...`
// invocations and validates that every prescribed flag is actually registered
// on the target cobra command. Prevents recurrence of bug-7ca3638b (skill
// prescribing `--format json` that trackShowCmd did not register).
//
// Scope: plugin/skills/**/*.md, plugin/commands/**/*.md, plugin/agents/**/*.md.
//
// Allowlist: flags listed in allowedShellFlags are skipped — these belong to
// piped-to tools (grep, jq, etc.) rather than the wipnote CLI.
func TestSkillFlagsIntegration(t *testing.T) {
	root := repoRootForTest(t)
	var pluginRoots []string
	for _, sub := range []string{"plugin/skills", "plugin/commands", "plugin/agents"} {
		p := filepath.Join(root, sub)
		if _, err := os.Stat(p); err == nil {
			pluginRoots = append(pluginRoots, p)
		}
	}
	if len(pluginRoots) == 0 {
		t.Skip("no plugin asset tree at repo root — skipping skill-flag validation")
	}

	cliRoot := buildRoot()
	var violations []string
	scanned := 0

	for _, pr := range pluginRoots {
		err := filepath.WalkDir(pr, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			scanned++
			violations = append(violations, scanFileForFlagViolations(t, path, cliRoot)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", pr, err)
		}
	}

	t.Logf("scanned %d markdown files", scanned)
	if len(violations) > 0 {
		for _, v := range violations {
			t.Error(v)
		}
	}
}

// TestSkillFlagsValidator_CatchesBadFixture exercises the flag-validation
// engine against a known-bad fixture so failures in the main scanner can be
// reproduced in isolation.
func TestSkillFlagsValidator_CatchesBadFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "skill_flag_bad.md")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	violations := scanFileForFlagViolations(t, fixture, buildRoot())
	if len(violations) == 0 {
		t.Fatal("scanner failed to flag known-bad fixture")
	}
}

// TestSkillFlagsValidator_PassesGoodFixture exercises the engine against a
// known-good fixture.
func TestSkillFlagsValidator_PassesGoodFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "skill_flag_good.md")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	violations := scanFileForFlagViolations(t, fixture, buildRoot())
	if len(violations) > 0 {
		t.Fatalf("clean fixture reported %d violations: %v", len(violations), violations)
	}
}

func TestPlanningSkillsAvoidClaudeOnlyCriticRunners(t *testing.T) {
	root := repoRootForTest(t)
	paths := []string{
		filepath.Join(root, "plugin", "skills", "plan", "SKILL.md"),
		filepath.Join(root, "plugin", "skills", "plan-critique", "SKILL.md"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, forbidden := range []string{"Sonnet+Haiku", "Haiku model", "Sonnet model", "source: haiku", "source: sonnet", "Haiku (design)", "Sonnet (feasibility)"} {
			if strings.Contains(content, forbidden) {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("%s contains Claude-only critique runner reference %q", rel, forbidden)
			}
		}
	}
}

func TestPlanningSkillsRequireResearchProvenance(t *testing.T) {
	root := repoRootForTest(t)
	checks := map[string][]string{
		filepath.Join(root, "plugin", "skills", "plan", "SKILL.md"): {
			"Mandatory Research",
			"Research basis",
			"External technical claims have a visible research basis",
		},
		filepath.Join(root, "plugin", "skills", "plan-critique", "SKILL.md"): {
			"Verify external claims via web search / web fetch",
			"Runtime verification requirements for backend claims",
			"unverified",
		},
	}
	for path, wants := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, want := range wants {
			if !strings.Contains(content, want) {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("%s missing research provenance marker %q", rel, want)
			}
		}
	}
}

func TestOrchestratorResearchDelegationContract(t *testing.T) {
	root := repoRootForTest(t)
	checks := map[string][]string{
		filepath.Join(root, "plugin", "skills", "orchestrator-directives-skill", "SKILL.md"): {
			"Harness-Aware Research Delegation Contract",
			"MUST run in a sidecar context",
			"Do not use main-context `web.search_query`",
			"delegation unavailable in this harness/session",
		},
		filepath.Join(root, "plugin", "skills", "agent-context", "SKILL.md"): {
			"For orchestrators:",
			"Research-first does not mean orchestrator researches directly",
			"Main-context `web.*` use is limited",
		},
	}
	for path, wants := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := string(data)
		for _, want := range wants {
			if !strings.Contains(content, want) {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("%s missing orchestrator research delegation marker %q", rel, want)
			}
		}
	}
}

func TestSharedPluginPromptsAvoidClaudeOnlyBoilerplate(t *testing.T) {
	root := repoRootForTest(t)
	pluginRoots := []string{
		filepath.Join(root, "plugin", "commands"),
		filepath.Join(root, "plugin", "skills"),
	}
	forbidden := []string{
		"## Instructions for Claude",
		"Claude performs the searches directly",
		"Claude analyzes the diff",
		"Claude Code version:",
		"Co-Authored-By: Claude",
	}

	for _, pr := range pluginRoots {
		err := filepath.WalkDir(pr, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content := string(data)
			for _, phrase := range forbidden {
				if strings.Contains(content, phrase) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s contains Claude-only shared prompt boilerplate %q", rel, phrase)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", pr, err)
		}
	}
}

// allowedShellFlags are flags that belong to non-wipnote tools reached via
// pipes in skill examples (grep, jq, etc.). They should not be validated
// against the cobra tree.
var allowedShellFlags = map[string]bool{
	"--line-buffered": true,
	"--raw-output":    true,
	"--compact":       true, // wipnote help --compact is real; also jq --compact
	"--quiet":         true,
	"--deep":          false, // real wipnote flag — leave to the validator
}

// invocationPattern matches `wipnote <cmd> [<subcmd>...]` followed by one or
// more `--flag` tokens. Terminates on newline, backtick, or pipe/redirect.
// Intentionally permissive — false-positive matches are filtered by the
// validator when the command path is unknown.
var invocationPattern = regexp.MustCompile(`\bwipnote[ \t]+([a-zA-Z][\w-]*(?:[ \t]+[a-zA-Z][\w-]*)*?)[ \t]+((?:--[a-zA-Z][\w-]*(?:[= \t]\S+)?[ \t]*)+)`)
var flagPattern = regexp.MustCompile(`--[a-zA-Z][\w-]*`)

func scanFileForFlagViolations(t *testing.T, path string, cliRoot *cobra.Command) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	var violations []string
	for _, match := range invocationPattern.FindAllStringSubmatchIndex(content, -1) {
		cmdStr := content[match[2]:match[3]]
		flagsStr := content[match[4]:match[5]]

		// Remove piped-to-tool tail: if flagsStr contains " | ", " > ", etc.,
		// strip everything after to avoid validating grep/jq flags.
		if idx := strings.IndexAny(flagsStr, "|>"); idx >= 0 {
			flagsStr = flagsStr[:idx]
		}

		targetCmd := resolveCobraCommand(cliRoot, strings.Fields(cmdStr))
		if targetCmd == nil {
			// Unknown command path — likely an illustrative example or a
			// command we don't care to validate (e.g. `wipnote feature`
			// standalone where the next word is a placeholder). Skip.
			continue
		}

		for _, flag := range flagPattern.FindAllString(flagsStr, -1) {
			if allowedShellFlags[flag] {
				continue
			}
			if !commandRegistersFlag(targetCmd, flag) {
				lineNum := lineNumberFor(lines, content, match[0])
				rel, _ := filepath.Rel(repoRootForTest(t), path)
				violations = append(violations,
					fmt.Sprintf("%s:%d: command %q does not register flag %s",
						rel, lineNum, "wipnote "+cmdStr, flag))
			}
		}
	}
	return violations
}

// resolveCobraCommand walks the cobra tree matching path tokens greedily. It
// returns the deepest command that all leading tokens resolve to; the first
// token that isn't a registered subcommand is treated as a positional
// argument (e.g. an <id>) and ends the walk. Returns nil if root is nil or
// the very first token doesn't resolve.
func resolveCobraCommand(root *cobra.Command, path []string) *cobra.Command {
	if root == nil {
		return nil
	}
	cur := root
	matched := false
	for _, token := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == token {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		cur = next
		matched = true
	}
	if !matched {
		return nil
	}
	return cur
}

// commandRegistersFlag returns true if cmd (or any of its parents) registers the
// given flag via Flags() or PersistentFlags().
func commandRegistersFlag(cmd *cobra.Command, flag string) bool {
	name := strings.TrimPrefix(flag, "--")
	for c := cmd; c != nil; c = c.Parent() {
		if c.Flags().Lookup(name) != nil {
			return true
		}
		if c.PersistentFlags().Lookup(name) != nil {
			return true
		}
	}
	return false
}

// lineNumberFor returns the 1-based line number for the byte offset within
// content. Used for nice error messages.
func lineNumberFor(_ []string, content string, offset int) int {
	if offset < 0 || offset >= len(content) {
		return 0
	}
	return strings.Count(content[:offset], "\n") + 1
}

// repoRootForTest walks upward from the test's working directory looking for a
// go.mod. The plugin asset tree lives at repo-root/plugin/... so tests need
// to resolve that path regardless of which package they're executed from.
// Wraps the package-internal findRepoRoot helper with a t.Fatal-on-error
// signature convenient for test code.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := findRepoRoot(cwd)
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	return root
}
