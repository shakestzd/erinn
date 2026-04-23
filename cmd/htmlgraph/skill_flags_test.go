package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// skillFlagMismatch records a flag found in a markdown file that is not
// registered on the target cobra command.
type skillFlagMismatch struct {
	file    string
	line    int
	cmdPath string
	flag    string
}

func (m skillFlagMismatch) String() string {
	return fmt.Sprintf("%s:%d: command %q does not register flag --%s", m.file, m.line, m.cmdPath, m.flag)
}

// invocationLinePattern matches lines/spans that start (or contain) an
// htmlgraph CLI invocation and captures everything after "htmlgraph" up to
// end-of-line or a backtick (inline code boundary).
var invocationLinePattern = regexp.MustCompile(`\bhtmlgraph\b([^\n` + "`" + `]*)`)

// flagPattern extracts individual --flag names from a string.
var flagPattern = regexp.MustCompile(`--([a-zA-Z][\w-]*)`)

// skipFilePattern matches paths inside .archived/ trees.
var skipFilePattern = regexp.MustCompile(`[/\\]\.archived[/\\]`)

// TestSkillFlagsUnit validates testdata fixtures.
// skill_flag_bad.md must produce mismatches; skill_flag_good.md must produce none.
func TestSkillFlagsUnit(t *testing.T) {
	root := buildRoot()

	t.Run("bad_fixture_triggers_error", func(t *testing.T) {
		badFile := filepath.Join("testdata", "skill_flag_bad.md")
		mismatches := scanFile(badFile, root)
		if len(mismatches) == 0 {
			t.Errorf("expected at least one mismatch in %s, got none", badFile)
		}
		found := false
		for _, m := range mismatches {
			if m.flag == "this-flag-doesnt-exist" {
				found = true
				t.Logf("correctly detected: %s", m)
			}
		}
		if !found {
			t.Errorf("expected mismatch for --this-flag-doesnt-exist; got: %v", mismatches)
		}
	})

	t.Run("good_fixture_passes_clean", func(t *testing.T) {
		goodFile := filepath.Join("testdata", "skill_flag_good.md")
		mismatches := scanFile(goodFile, root)
		for _, m := range mismatches {
			t.Errorf("unexpected mismatch in good fixture: %s", m)
		}
	})
}

// TestSkillFlagsIntegration walks all plugin markdown files and fails on any
// htmlgraph invocation whose flags are not registered on the target command.
func TestSkillFlagsIntegration(t *testing.T) {
	pluginRoot := filepath.Join("..", "..", "plugin")
	if _, err := os.Stat(pluginRoot); err != nil {
		t.Skipf("plugin/ directory not found at %s: %v", pluginRoot, err)
	}

	root := buildRoot()

	var mdFiles []string
	scanDirs := []string{
		filepath.Join(pluginRoot, "skills"),
		filepath.Join(pluginRoot, "commands"),
		filepath.Join(pluginRoot, "agents"),
	}
	for _, dir := range scanDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".md") && !skipFilePattern.MatchString(path) {
				mdFiles = append(mdFiles, path)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(mdFiles) == 0 {
		t.Fatal("no markdown files found in plugin/")
	}
	t.Logf("scanning %d markdown files", len(mdFiles))

	var allMismatches []skillFlagMismatch
	for _, f := range mdFiles {
		allMismatches = append(allMismatches, scanFile(f, root)...)
	}

	for _, m := range allMismatches {
		t.Errorf("%s", m)
	}
}

// scanFile reads a markdown file and returns all flag mismatches found.
func scanFile(path string, root *cobra.Command) []skillFlagMismatch {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return scanContent(path, string(data), root)
}

// scanContent parses markdown content for htmlgraph CLI invocations and
// cross-references each flag against the cobra command tree.
func scanContent(filename, content string, root *cobra.Command) []skillFlagMismatch {
	lines := strings.Split(content, "\n")
	var mismatches []skillFlagMismatch

	for lineNum, line := range lines {
		// Strip inline code backticks — keeps content, removes delimiters.
		stripped := strings.ReplaceAll(line, "`", " ")

		// Find all htmlgraph invocations on this line.
		invMatches := invocationLinePattern.FindAllStringSubmatch(stripped, -1)
		for _, invMatch := range invMatches {
			if len(invMatch) < 2 {
				continue
			}
			rest := invMatch[1]

			// Parse leading command words (cobra subcommand path).
			cmdWords := parseCommandWords(rest)

			// Skip invocations with placeholders in the command path.
			if containsPlaceholder(cmdWords) {
				continue
			}

			// Skip bare "htmlgraph --help" / "htmlgraph --version" invocations.
			if len(cmdWords) == 0 {
				continue
			}

			// Find the target cobra command.
			cmd := findCobraCmd(root, cmdWords)
			if cmd == nil {
				continue
			}

			// Extract and validate flags.
			flagMatches := flagPattern.FindAllStringSubmatch(rest, -1)
			for _, fm := range flagMatches {
				if len(fm) < 2 {
					continue
				}
				flagName := fm[1]
				if isAllowlistedExternalFlag(flagName) {
					continue
				}
				if !cmdHasFlag(cmd, flagName) {
					mismatches = append(mismatches, skillFlagMismatch{
						file:    filename,
						line:    lineNum + 1,
						cmdPath: cmd.CommandPath(),
						flag:    flagName,
					})
				}
			}
		}
	}
	return mismatches
}

// parseCommandWords extracts the leading cobra subcommand words from the
// remainder of an htmlgraph invocation. It stops at the first flag (--),
// quoted argument, placeholder, shell variable, or ID-like token (contains digits).
func parseCommandWords(rest string) []string {
	words := strings.Fields(rest)
	var cmds []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			break // flag starts — done with command path
		}
		if !isPureCmdWord(w) {
			break // quoted arg, placeholder, ID, variable — stop
		}
		cmds = append(cmds, w)
	}
	return cmds
}

// isPureCmdWord returns true if w could be a cobra subcommand name: only
// lowercase/uppercase letters and hyphens, no digits, no special chars.
func isPureCmdWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '-' {
			return false
		}
	}
	return true
}

// containsPlaceholder returns true if any word looks like a template placeholder
// (e.g. {feature_id}, <id>).
func containsPlaceholder(words []string) bool {
	for _, w := range words {
		if strings.ContainsAny(w, "{}") || (strings.HasPrefix(w, "<") && strings.HasSuffix(w, ">")) {
			return true
		}
	}
	return false
}

// findCobraCmd traverses the cobra command tree following cmdWords, returning
// the deepest matched command. Returns nil if no subcommand is found.
func findCobraCmd(root *cobra.Command, words []string) *cobra.Command {
	if len(words) == 0 {
		return nil
	}
	current := root
	for _, word := range words {
		found := false
		for _, sub := range current.Commands() {
			if sub.Name() == word {
				current = sub
				found = true
				break
			}
		}
		if !found {
			// Stop here — remaining words are positional args, not subcommands.
			break
		}
	}
	if current == root {
		return nil
	}
	return current
}

// cmdHasFlag returns true if flagName is registered on cmd, its persistent
// flags, or any ancestor's persistent flags.
func cmdHasFlag(cmd *cobra.Command, flagName string) bool {
	if cmd.Flags().Lookup(flagName) != nil {
		return true
	}
	if cmd.PersistentFlags().Lookup(flagName) != nil {
		return true
	}
	for p := cmd.Parent(); p != nil; p = p.Parent() {
		if p.PersistentFlags().Lookup(flagName) != nil {
			return true
		}
	}
	return false
}

// isAllowlistedExternalFlag returns true for flags that belong to shell
// utilities (grep, git, curl, etc.) rather than our CLI.
func isAllowlistedExternalFlag(flag string) bool {
	// These flag names appear in skill docs as part of shell pipeline commands
	// (e.g. grep --line-buffered, git --no-verify) and must not be validated
	// against our cobra tree.
	external := map[string]bool{
		// grep / ripgrep
		"line-buffered": true,
		"only-matching": true,
		"color":         true,
		// git
		"no-verify":    true,
		"gpg-sign":     true,
		"no-edit":      true,
		"squash":       true,
		"onto":         true,
		"hard":         true,
		"soft":         true,
		"mixed":        true,
		"force":        true,
		"force-update": true,
		"set-upstream": true,
		// curl
		"silent":      true,
		"location":    true,
		"output":      true,
		"header":      true,
		"data":        true,
		"user":        true,
		// jq
		"raw-output": true,
		"arg":        true,
		// tmux / shell builtins
		"no-confirm": true,
		// go test
		"count": true,
		"run":   true,
	}
	return external[flag]
}
