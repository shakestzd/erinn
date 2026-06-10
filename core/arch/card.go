// Package arch provides parse, validate, and lifecycle operations for
// architectural memory cards stored as .wipnote/arch/<slug>.md files.
//
// Design constraints (slice 1):
//   - No internal/ imports — pure core package.
//   - Body cap is in words (120 max), not tokens.
//   - Glob patterns are stored as plain strings; matching lives in slice 2.
package arch

import (
	"bytes"
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
	// Frontmatter fields.
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

	// Body is the markdown content after the frontmatter.
	Body string `yaml:"-"`
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

// Parse parses a card from raw markdown bytes with YAML frontmatter.
// The expected format is:
//
//	---
//	name: slug
//	kind: invariant
//	...
//	---
//	Body text here.
func Parse(data []byte) (*Card, error) {
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

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
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

// Marshal renders a card back to its canonical markdown-with-frontmatter format.
func Marshal(c *Card) ([]byte, error) {
	fm, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fm)
	buf.WriteString("---\n")
	if c.Body != "" {
		buf.WriteString("\n")
		buf.WriteString(c.Body)
		buf.WriteString("\n")
	}
	return buf.Bytes(), nil
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
