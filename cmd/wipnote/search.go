package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

const (
	searchDefaultLimit  = 50
	searchMaxSnippetLen = 200
	searchMissingBinMsg = "ast-grep not found on PATH — install from https://ast-grep.github.io/ or run: brew install ast-grep / cargo install ast-grep"
)

// astGrepMatch is the JSON structure emitted by ast-grep run --json.
type astGrepMatch struct {
	File  string `json:"file"`
	Range struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
	Lines string `json:"lines"` // matched source text
	Text  string `json:"text"`  // alternate field in some versions
}

// searchOpts holds the parsed CLI flags for wipnote search.
type searchOpts struct {
	lang  string
	path  string
	limit int
	json  bool
}

// searchCmd returns the cobra command for `wipnote search`.
func searchCmd() *cobra.Command {
	var opts searchOpts

	cmd := &cobra.Command{
		Use:   "search <pattern>",
		Short: "AST-aware structural code search via ast-grep",
		Long: `Run a structural code search using ast-grep.

Pattern syntax follows ast-grep's pattern language (see https://ast-grep.github.io/).

Output (default, one line per match):
  <file>:<line>: <captured snippet>

Examples:
  wipnote search 'func $NAME() error'
  wipnote search 'func $NAME() error' --lang go
  wipnote search 'console.log($$$)' --lang js --path ./frontend
  wipnote search 'fmt.Println($$$)' --limit 10
  wipnote search 'func $NAME($$$)' --json`,
		Args: cobra.ExactArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if _, err := exec.LookPath("ast-grep"); err != nil {
				fmt.Fprintln(os.Stderr, searchMissingBinMsg)
				os.Exit(2)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			return runSearch(args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.lang, "lang", "", "Language hint passed to ast-grep (-l flag)")
	cmd.Flags().StringVar(&opts.path, "path", ".", "Restrict search to this path (file or directory)")
	cmd.Flags().IntVar(&opts.limit, "limit", searchDefaultLimit, "Maximum number of matches to return (0 = unlimited)")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Emit JSON lines instead of human-readable format")

	return cmd
}

// runSearch executes ast-grep with the given pattern and options, then formats
// and prints the results. Exit codes: 0 = matches, 1 = no matches, 2 = error.
func runSearch(pattern string, opts searchOpts) error {
	args := buildAstGrepArgs(pattern, opts)
	raw, err := exec.Command("ast-grep", args...).Output() //nolint:gosec
	if err != nil {
		// ast-grep exits 1 when no matches; that is not an error we surface.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			if opts.json {
				fmt.Println("[]")
			} else {
				fmt.Println("No matches found.")
			}
			return nil
		}
		return fmt.Errorf("ast-grep: %w", err)
	}

	matches, parseErr := parseAstGrepJSON(raw)
	if parseErr != nil {
		return fmt.Errorf("parse ast-grep output: %w", parseErr)
	}

	if len(matches) == 0 {
		if opts.json {
			fmt.Println("[]")
		} else {
			fmt.Println("No matches found.")
		}
		return nil
	}

	truncated := false
	if opts.limit > 0 && len(matches) > opts.limit {
		matches = matches[:opts.limit]
		truncated = true
	}

	if opts.json {
		printSearchJSON(matches)
	} else {
		printSearchHuman(matches)
	}

	if truncated {
		fmt.Fprintf(os.Stderr, "... (truncated, run with --limit to see more)\n")
	}

	return nil
}

// buildAstGrepArgs constructs the argument slice for ast-grep.
func buildAstGrepArgs(pattern string, opts searchOpts) []string {
	args := []string{"run", "-p", pattern, "--json"}
	if opts.lang != "" {
		args = append(args, "-l", opts.lang)
	}
	if opts.path != "" && opts.path != "." {
		args = append(args, opts.path)
	}
	return args
}

// parseAstGrepJSON parses newline-delimited or array JSON from ast-grep.
// ast-grep --json emits either a JSON array or one object per line depending
// on version; this handles both.
func parseAstGrepJSON(raw []byte) ([]astGrepMatch, error) {
	data := strings.TrimSpace(string(raw))
	if data == "" || data == "[]" || data == "null" {
		return nil, nil
	}

	// Try array form first (most common in recent ast-grep versions).
	if strings.HasPrefix(data, "[") {
		var matches []astGrepMatch
		if err := json.Unmarshal([]byte(data), &matches); err == nil {
			return matches, nil
		}
	}

	// Fall back to newline-delimited JSON objects.
	var matches []astGrepMatch
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "[" || line == "]" {
			continue
		}
		line = strings.TrimRight(line, ",")
		var m astGrepMatch
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // skip malformed lines
		}
		matches = append(matches, m)
	}
	return matches, scanner.Err()
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// collapseSnippet strips ANSI codes, collapses whitespace, and caps length.
func collapseSnippet(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > searchMaxSnippetLen {
		s = s[:searchMaxSnippetLen-1] + "…"
	}
	return s
}

func snippet(m astGrepMatch) string {
	if m.Lines != "" {
		return collapseSnippet(m.Lines)
	}
	return collapseSnippet(m.Text)
}

func printSearchHuman(matches []astGrepMatch) {
	for _, m := range matches {
		fmt.Printf("%s:%d: %s\n", m.File, m.Range.Start.Line+1, snippet(m))
	}
}

func printSearchJSON(matches []astGrepMatch) {
	for _, m := range matches {
		obj := map[string]any{
			"file":    m.File,
			"line":    m.Range.Start.Line + 1,
			"snippet": snippet(m),
		}
		data, _ := json.Marshal(obj)
		fmt.Println(string(data))
	}
}
