// Register in main.go: rootCmd.AddCommand(tddCmd())
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// acPattern matches numbered acceptance criteria lines: "1. [ ] description"
var acPattern = regexp.MustCompile(`^\s*\d+\.\s*\[\s*[xX ]?\s*\]\s*(.+)$`)

func tddCmd() *cobra.Command {
	var python bool
	var output string
	var pkg string

	cmd := &cobra.Command{
		Use:   "tdd <feature-id>",
		Short: "Generate test stubs from a feature spec's acceptance criteria",
		Long: `Read acceptance criteria from a feature spec and output test function stubs.

Agents fill in test bodies before implementing — enforcing TDD (red-green-refactor).

The command sources criteria from two places (in priority order):
  1. <section class="spec"> in the feature HTML (written by wipnote spec generate)
  2. Steps listed in <section data-steps> as a fallback

Example:
  wipnote tdd feat-abc123
  wipnote tdd feat-abc123 --python
  wipnote tdd feat-abc123 --output tests/feat_abc123_test.go
  wipnote tdd feat-abc123 --package mypackage_test`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTDD(args[0], python, output, pkg)
		},
	}

	cmd.Flags().BoolVar(&python, "python", false, "Generate Python tests instead of Go")
	cmd.Flags().StringVar(&output, "output", "", "Write to file instead of stdout")
	cmd.Flags().StringVar(&pkg, "package", "main_test", "Go package name (ignored for --python)")
	return cmd
}

// runTDD loads the feature, extracts acceptance criteria, and renders test stubs.
func runTDD(featureID string, python bool, output, pkg string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, "features", featureID+".html")
	if _, err := os.Stat(path); err != nil {
		return workitem.ErrNotFound("feature", featureID)
	}

	criteria, err := extractAcceptanceCriteria(path)
	if err != nil {
		return fmt.Errorf("extract acceptance criteria: %w", err)
	}

	if len(criteria) == 0 {
		return fmt.Errorf("no acceptance criteria found in %s — run: wipnote spec generate %s", featureID, featureID)
	}

	var content string
	if python {
		content = renderPythonTests(criteria)
	} else {
		content = renderGoTests(criteria, pkg)
	}

	return writeTDDOutput(content, output)
}

// extractAcceptanceCriteria reads a feature HTML file and returns acceptance
// criteria strings. It checks the spec section first, then falls back to steps.
func extractAcceptanceCriteria(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	criteria := extractFromSpecSection(string(raw))
	if len(criteria) > 0 {
		return criteria, nil
	}

	return extractFromSteps(path)
}

// extractFromSpecSection extracts acceptance criteria from the
// <section class="spec"> block. It supports both the legacy numbered
// checkbox format (`1. [ ] ...`) and the OpenSpec Requirement / Scenario
// format (`### Requirement: <name>` blocks) by routing through the shared
// parseCriteria helper. The `<pre>` wrapper applied by `spec generate
// --insert` is unwrapped before parsing.
func extractFromSpecSection(html string) []string {
	const open = `<section class="spec">`
	const close = `</section>`

	start := strings.Index(html, open)
	if start == -1 {
		return nil
	}
	end := strings.Index(html[start:], close)
	if end == -1 {
		return nil
	}
	section := html[start+len(open) : start+end]

	// Try the unified parser first (handles both legacy and OpenSpec).
	parsed := parseCriteria(unwrapPreBlock(section))
	if len(parsed) > 0 {
		out := make([]string, 0, len(parsed))
		for _, c := range parsed {
			text := strings.TrimSpace(c.Text)
			if text != "" {
				out = append(out, text)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	// Fallback: tolerate the original legacy numbered-checkbox layout in
	// case parseCriteria's section-state machine doesn't recognise the
	// heading style (e.g. specs that omit `## Acceptance Criteria`).
	var criteria []string
	for _, line := range strings.Split(section, "\n") {
		m := acPattern.FindStringSubmatch(line)
		if m != nil {
			text := strings.TrimSpace(m[1])
			if text != "" {
				criteria = append(criteria, text)
			}
		}
	}
	return criteria
}

// extractFromSteps uses htmlparse to read the node's steps as acceptance criteria.
func extractFromSteps(path string) ([]string, error) {
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var criteria []string
	for _, s := range node.Steps {
		text := strings.TrimSpace(s.Description)
		if text != "" {
			criteria = append(criteria, text)
		}
	}
	return criteria, nil
}

// renderGoTests produces Go test stubs, one per criterion.
func renderGoTests(criteria []string, pkg string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("package %s\n\n", pkg))
	sb.WriteString("import \"testing\"\n")

	for _, c := range criteria {
		name := toGoTestName(c)
		sb.WriteString(fmt.Sprintf("\nfunc %s(t *testing.T) {\n", name))
		sb.WriteString(fmt.Sprintf("\tt.Skip(\"TODO: implement — %s\")\n", escapeDQ(c)))
		sb.WriteString("}\n")
	}
	return sb.String()
}

// renderPythonTests produces pytest stubs, one per criterion.
func renderPythonTests(criteria []string) string {
	var sb strings.Builder
	sb.WriteString("import pytest\n")

	for _, c := range criteria {
		name := toPythonTestName(c)
		sb.WriteString(fmt.Sprintf("\n\ndef %s():\n", name))
		sb.WriteString(fmt.Sprintf("    \"\"\"%s\"\"\"\n", escapeDQ(c)))
		sb.WriteString("    pytest.skip(\"TODO: implement\")\n")
	}
	return sb.String()
}

// toGoTestName converts a criterion string to a valid Go test function name.
// e.g. "wipnote review shows diff summary" → "TestWipnoteReviewShowsDiffSummary"
func toGoTestName(s string) string {
	words := splitWords(s)
	var sb strings.Builder
	sb.WriteString("Test")
	for _, w := range words {
		if w == "" {
			continue
		}
		runes := []rune(w)
		sb.WriteRune(unicode.ToUpper(runes[0]))
		sb.WriteString(string(runes[1:]))
	}
	return sb.String()
}

// toPythonTestName converts a criterion string to a snake_case test name.
// e.g. "wipnote review shows diff" → "test_wipnote_review_shows_diff"
func toPythonTestName(s string) string {
	words := splitWords(s)
	return "test_" + strings.Join(words, "_")
}

// splitWords splits s into lower-case identifier words, stripping punctuation.
func splitWords(s string) []string {
	// Replace non-alphanumeric runs with spaces, then split.
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf.WriteRune(unicode.ToLower(r))
		} else {
			buf.WriteRune(' ')
		}
	}
	parts := strings.Fields(buf.String())
	return parts
}

// escapeDQ escapes double-quote characters for use inside double-quoted strings.
func escapeDQ(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// writeTDDOutput writes content to a file or stdout.
func writeTDDOutput(content, path string) error {
	if path == "" {
		fmt.Println(content)
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("Tests written to %s\n", path)
	return nil
}
