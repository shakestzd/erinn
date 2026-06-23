// Package arch provides parse, validate, and lifecycle operations for
// architectural memory cards that are canonically stored in
// .wipnote/architecture.html, while remaining backward-compatible with
// legacy/import file formats under .wipnote/arch/.
//
// Design constraints (slice 1):
//   - No internal/ imports — pure core package.
//   - Body cap is in words (120 max), not tokens.
//   - Glob patterns are stored as plain strings; matching lives in slice 2.
package arch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Kind enumerates the allowed card kinds.
type Kind string

const (
	KindSubsystemMap Kind = "subsystem-map"
	KindInvariant    Kind = "invariant"
	KindHazard       Kind = "hazard"
	KindDecision     Kind = "decision"
)

// validKinds is the complete set of allowed Kind values.
var validKinds = map[Kind]bool{
	KindSubsystemMap: true,
	KindInvariant:    true,
	KindHazard:       true,
	KindDecision:     true,
}

// MaxBodyWords is the maximum number of words allowed in a card body.
const MaxBodyWords = 120

// Card represents a parsed and validated architectural memory card.
type Card struct {
	// Structured fields.
	Name         string    `yaml:"name"`
	Kind         Kind      `yaml:"kind"`
	Paths        []string  `yaml:"paths"`
	VerifiedAt   string    `yaml:"verified_at"`
	Links        []string  `yaml:"links"`
	CreatedBy    string    `yaml:"created_by"`
	SupersededBy string    `yaml:"superseded_by,omitempty"`
	Retired      bool      `yaml:"retired,omitempty"`
	CreatedAt    time.Time `yaml:"created_at,omitempty"`
	UpdatedAt    time.Time `yaml:"updated_at,omitempty"`

	// Body is markdown content stored explicitly in the canonical HTML ledger and
	// synthesized from the trailing markdown section in legacy/import frontmatter cards.
	Body string `yaml:"body"`
}

// IsRetired reports whether the card has been superseded or explicitly retired.
// A card is retired when SupersededBy is set OR the Retired flag is true.
func (c *Card) IsRetired() bool {
	return c.Retired || c.SupersededBy != ""
}

// ParseFile reads and parses a card from the file at path.
// It returns ErrNotFound when the file does not exist.
func ParseFile(path string) (*Card, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read card file: %w", err)
	}
	card, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return card, nil
}

// Parse parses either a canonical ledger-backed card or a legacy/import
// markdown card with YAML frontmatter. The legacy format is:
//
//	---
//	name: slug
//	kind: invariant
//	...
//	---
//	Body text here.
func Parse(data []byte) (*Card, error) {
	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(content, "---") {
		var card Card
		if err := yaml.Unmarshal(data, &card); err != nil {
			return nil, fmt.Errorf("parse yaml card: %w", err)
		}
		card.Body = strings.TrimSpace(card.Body)
		return &card, nil
	}

	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	var card Card
	if err := yaml.Unmarshal(fm, &card); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	card.Body = strings.TrimSpace(body)
	return &card, nil
}

// Validate checks all invariants on a parsed card and returns a joined error
// listing every violation found. Returns nil when the card is valid.
//
// Path validation rules (per bug-c06a0457):
//
//	ERROR (fails validation):
//	- filepath.IsAbs(path) — absolute paths are host-local and not portable
//	- strings.HasPrefix(path, "unresolved:") — outside-repo sentinel
//	- path == ".." or strings.HasPrefix(path, "../") — repo-escape via traversal
//
//	WARN (advisory, not an error — use ValidatePaths for warnings):
//	- path contains /tmp/ — temp file captured by mistake
//	- path contains agent-memory — agent session artifact
//	- path contains .claude/worktrees — dead worktree path
func Validate(c *Card) error {
	var errs []string

	if c.Name == "" {
		errs = append(errs, "name is required")
	} else if !isValidSlug(c.Name) {
		errs = append(errs, "name must be a valid slug (lowercase letters, digits, hyphens)")
	}

	if c.Kind == "" {
		errs = append(errs, "kind is required")
	} else if !validKinds[c.Kind] {
		errs = append(errs, fmt.Sprintf("kind %q is not valid; must be one of: subsystem-map, invariant, hazard, decision", c.Kind))
	}

	if c.CreatedBy == "" {
		errs = append(errs, "created_by is required")
	}

	wc := countWords(c.Body)
	if wc > MaxBodyWords {
		errs = append(errs, fmt.Sprintf("body exceeds %d-word limit (%d words)", MaxBodyWords, wc))
	}

	if c.SupersededBy != "" && !isValidSlug(c.SupersededBy) {
		errs = append(errs, "superseded_by must be a valid slug")
	}

	for _, p := range c.Paths {
		if pe := validatePathError(p); pe != "" {
			errs = append(errs, pe)
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

// validatePathError returns a non-empty error string when path p violates an
// ERROR-class path rule (absolute, unresolved:, or ../ escape).
// Returns "" when p is acceptable.
func validatePathError(p string) string {
	if filepath.IsAbs(p) {
		return fmt.Sprintf("path %q is absolute; only repo-relative paths are allowed", p)
	}
	if strings.HasPrefix(p, "unresolved:") {
		return fmt.Sprintf("path %q has unresolved: prefix; outside-repo paths must be excluded", p)
	}
	// Normalize before the escape check so INTERIOR dot-dot segments cannot
	// slip past a naive prefix test (bug-fddf5820, finding 9). For example
	// "a/../../etc" has no "../" prefix but resolves to "../etc", which escapes
	// the repo root. filepath.Clean collapses the path so any residual leading
	// ".." surfaces the escape.
	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Sprintf("path %q escapes the repository via ..; only repo-relative paths are allowed", p)
	}
	return ""
}

// ValidatePaths returns WARNING strings for paths that are suspicious but not
// errors — temp files, agent memory artifacts, dead worktree paths. Returns nil
// when no warnings apply. These do not fail Validate; callers should emit them
// to stderr.
func ValidatePaths(paths []string) []string {
	var warns []string
	for _, p := range paths {
		if w := validatePathWarn(p); w != "" {
			warns = append(warns, w)
		}
	}
	return warns
}

// validatePathWarn returns a non-empty warning string when path p matches a
// WARN-class pattern (tmp, agent-memory, dead worktree). Returns "" otherwise.
func validatePathWarn(p string) string {
	switch {
	case strings.Contains(p, "/tmp/") || strings.HasPrefix(p, "tmp/"):
		return fmt.Sprintf("path %q looks like a temp file (contains /tmp/)", p)
	case strings.Contains(p, "agent-memory"):
		return fmt.Sprintf("path %q looks like an agent memory artifact", p)
	case strings.Contains(p, ".claude/worktrees"):
		return fmt.Sprintf("path %q looks like a dead worktree path", p)
	}
	return ""
}

// ParseAndValidate parses and validates in one step.
func ParseAndValidate(data []byte) (*Card, error) {
	card, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := Validate(card); err != nil {
		return nil, err
	}
	return card, nil
}

// Marshal renders a card to the import-compatible YAML representation used by
// the legacy file-based arch format.
func Marshal(c *Card) ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml card: %w", err)
	}
	return data, nil
}

// splitFrontmatter separates YAML frontmatter from body.
// Expects the document to start with "---\n".
func splitFrontmatter(data []byte) (fm []byte, body string, err error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, "", fmt.Errorf("card file must begin with YAML frontmatter (---)")
	}

	// Find the closing delimiter.
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, "", fmt.Errorf("frontmatter closing delimiter not found")
	}

	fmStr := strings.TrimSpace(rest[:idx])
	bodyStr := rest[idx+4:] // skip "\n---"
	// Skip an optional newline after the closing delimiter.
	bodyStr = strings.TrimPrefix(bodyStr, "\n")

	return []byte(fmStr), bodyStr, nil
}

// countWords counts whitespace-separated words in s.
func countWords(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	count := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inWord = false
		} else {
			if !inWord {
				count++
				inWord = true
			}
		}
	}
	return count
}

// isValidSlug returns true when s consists only of lowercase letters, digits,
// and hyphens, is non-empty, and does not start or end with a hyphen.
func isValidSlug(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' {
			return false
		}
	}
	return true
}

// ErrNotFound is returned when a card file does not exist.
var ErrNotFound = errors.New("card not found")

// ErrDuplicateSlug is returned when a card with the same name already exists.
var ErrDuplicateSlug = errors.New("card with this slug already exists")

// ErrDuplicateGlobSet is returned when a card with the exact same (non-empty)
// set of path globs already exists. Order of globs is ignored.
var ErrDuplicateGlobSet = errors.New("card with the same path glob set already exists")
