// Package arch implements the resolve engine for architectural memory cards.
//
// It matches card paths against a set of input file paths using the same glob
// semantics as internal/workowners, groups matched cards by verified_at SHA,
// runs ONE git diff per unique SHA, and renders the matched cards with drift
// annotations and a word-budget sentinel.
//
// Glob semantics are pinned to matchPattern / matchDoubleStar from
// internal/workowners — do NOT add a second glob standard.
package arch

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	corearch "github.com/shakestzd/wipnote/core/arch"
)

// kindPriority returns a numeric rank for ordering: lower = higher priority.
// Order: hazard(0) > invariant(1) > subsystem-map(2) > decision(3).
func kindPriority(k corearch.Kind) int {
	switch k {
	case corearch.KindHazard:
		return 0
	case corearch.KindInvariant:
		return 1
	case corearch.KindSubsystemMap:
		return 2
	case corearch.KindDecision:
		return 3
	default:
		return 4
	}
}

// SortByKindPriority sorts cards in-place: hazard > invariant > subsystem-map > decision.
// Cards with equal kind are sorted by name for determinism.
func SortByKindPriority(cards []*corearch.Card) {
	sort.SliceStable(cards, func(i, j int) bool {
		pi, pj := kindPriority(cards[i].Kind), kindPriority(cards[j].Kind)
		if pi != pj {
			return pi < pj
		}
		return cards[i].Name < cards[j].Name
	})
}

// GlobMatch reports whether path matches glob using workowners-compatible semantics:
//   - "**" matches zero or more path segments.
//   - "*" matches within a single segment only (does not cross "/").
//   - Matching is against forward-slash separated paths.
//
// Pinned to the same primitive as internal/workowners.matchPattern.
func GlobMatch(glob, path string) bool {
	if strings.Contains(glob, "**") {
		return matchDoubleStar(glob, path)
	}
	if matched, _ := filepath.Match(filepath.ToSlash(glob), filepath.ToSlash(path)); matched {
		return true
	}
	// No "/" in glob: also try basename match (e.g. "*.go" matches "dir/foo.go").
	if !strings.Contains(glob, "/") {
		base := filepath.Base(path)
		matched, _ := filepath.Match(glob, base)
		return matched
	}
	return false
}

// matchDoubleStar handles globs containing "**".
func matchDoubleStar(glob, path string) bool {
	// Bare "**" matches everything.
	if glob == "**" {
		return true
	}
	if strings.HasSuffix(glob, "/**") {
		prefix := glob[:len(glob)-3]
		return strings.HasPrefix(path, prefix+"/") || path == prefix
	}
	if strings.HasPrefix(glob, "**/") {
		suffix := glob[3:]
		return matchSegmentSuffix(suffix, path)
	}
	if idx := strings.Index(glob, "/**/"); idx >= 0 {
		prefix := glob[:idx]
		suffix := glob[idx+4:]
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
		rest := path[len(prefix)+1:]
		return matchSegmentSuffix(suffix, rest)
	}
	return false
}

// matchSegmentSuffix checks if suffix (which may contain "*") matches the tail
// segments of path.
func matchSegmentSuffix(suffix, path string) bool {
	suffixParts := strings.Split(suffix, "/")
	pathParts := strings.Split(path, "/")
	if len(suffixParts) > len(pathParts) {
		return false
	}
	tail := pathParts[len(pathParts)-len(suffixParts):]
	for i, sp := range suffixParts {
		matched, _ := filepath.Match(sp, tail[i])
		if !matched {
			return false
		}
	}
	return true
}

// cardMatchesPaths reports whether any of the card's path globs match any of
// the given file paths.
func cardMatchesPaths(card *corearch.Card, paths []string) bool {
	for _, glob := range card.Paths {
		for _, p := range paths {
			if GlobMatch(glob, p) {
				return true
			}
		}
	}
	return false
}

// MatchCards returns the subset of cards whose path globs match at least one
// of the given file paths. Retired and superseded cards are silently excluded.
func MatchCards(cards []*corearch.Card, paths []string) []*corearch.Card {
	var out []*corearch.Card
	for _, c := range cards {
		if c.IsRetired() {
			continue
		}
		if len(c.Paths) == 0 {
			continue
		}
		if cardMatchesPaths(c, paths) {
			out = append(out, c)
		}
	}
	return out
}

// GroupByVerifiedAt groups cards by their VerifiedAt field.
// Cards with an empty VerifiedAt are grouped under the "" key.
func GroupByVerifiedAt(cards []*corearch.Card) map[string][]*corearch.Card {
	out := make(map[string][]*corearch.Card)
	for _, c := range cards {
		out[c.VerifiedAt] = append(out[c.VerifiedAt], c)
	}
	return out
}

// DiffRunner runs git diff --name-only for a given SHA range.
// Abstracted for testability (callers may inject a stub).
type DiffRunner func(sha, repoRoot string) ([]string, error)

// GitDiffNameOnly executes ONE git diff --name-only <sha>..HEAD and returns
// the list of changed file paths (repo-relative, forward-slash).
func GitDiffNameOnly(sha, repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-only", sha+"..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s..HEAD: %w", sha, err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// DetectDrift returns a map from card.Name to the 7-char short SHA for each
// card whose globs overlap with files changed since verified_at. Cards with an
// empty verified_at are treated as unverified (mapped to "" key — caller checks
// card.VerifiedAt directly). Exactly ONE git diff invocation is made per
// unique non-empty verified_at value.
func DetectDrift(cards []*corearch.Card, repoRoot string, runner DiffRunner) map[string]string {
	groups := GroupByVerifiedAt(cards)
	driftMap := make(map[string]string) // card.Name → short SHA (may be "")

	// Cards with empty verified_at are unconditionally unverified.
	for _, c := range groups[""] {
		driftMap[c.Name] = ""
	}

	// For each unique non-empty SHA, run ONE git diff.
	for sha, group := range groups {
		if sha == "" {
			continue
		}
		changedFiles, err := runner(sha, repoRoot)
		if err != nil {
			// On error, treat all cards in this group as unverified.
			for _, c := range group {
				driftMap[c.Name] = sha[:min7(sha)]
			}
			continue
		}
		short := sha[:min7(sha)]
		for _, c := range group {
			if cardMatchesPaths(c, changedFiles) {
				driftMap[c.Name] = short
			}
			// If no match, card is not in driftMap — caller interprets absence as clean.
		}
	}
	return driftMap
}

func min7(s string) int {
	if len(s) < 7 {
		return len(s)
	}
	return 7
}

// BudgetResult holds the output of ApplyBudget.
type BudgetResult struct {
	Emitted []*corearch.Card
	Omitted int
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

// cardWordCount returns the word count for a card's body (the budget-relevant text).
func cardWordCount(c *corearch.Card) int {
	return countWords(c.Body)
}

// ApplyBudget emits cards in order until the next card would exceed the word
// budget. The first card always emits even if it alone exceeds the budget.
// Returns the emitted slice and a count of omitted cards.
func ApplyBudget(cards []*corearch.Card, budget int) BudgetResult {
	var emitted []*corearch.Card
	used := 0
	for i, c := range cards {
		wc := cardWordCount(c)
		if i == 0 || used+wc <= budget {
			emitted = append(emitted, c)
			used += wc
		} else {
			// This card would exceed budget — stop.
			return BudgetResult{
				Emitted: emitted,
				Omitted: len(cards) - len(emitted),
			}
		}
	}
	return BudgetResult{Emitted: emitted, Omitted: 0}
}

// RenderCard renders a single card as plain text suitable for pasting into an
// agent prompt. driftShort is the 7-char SHA if the card is drifted, or ""
// if the card has no drift. Cards with empty VerifiedAt are always rendered as
// UNVERIFIED.
func RenderCard(card *corearch.Card, driftShort string) string {
	var sb strings.Builder
	if driftShort != "" {
		fmt.Fprintf(&sb, "UNVERIFIED since %s — ", driftShort)
	} else if card.VerifiedAt == "" {
		sb.WriteString("UNVERIFIED — ")
	}
	fmt.Fprintf(&sb, "%s [%s]\n", card.Name, card.Kind)
	if card.Body != "" {
		sb.WriteString(card.Body)
		sb.WriteString("\n")
	}
	return sb.String()
}

// FormatOutput renders the full resolve output: cards in kind-priority order
// within the budget, with drift annotations and an omission sentinel when any
// cards are dropped. driftMap maps card.Name to a short SHA (present = drifted,
// absent = clean, "" value = unverified due to empty verified_at).
func FormatOutput(cards []*corearch.Card, budget int, driftMap map[string]string) string {
	sorted := make([]*corearch.Card, len(cards))
	copy(sorted, cards)
	SortByKindPriority(sorted)

	result := ApplyBudget(sorted, budget)

	var sb strings.Builder
	for _, c := range result.Emitted {
		short, isDrifted := driftMap[c.Name]
		if isDrifted {
			sb.WriteString(RenderCard(c, short))
		} else {
			sb.WriteString(RenderCard(c, ""))
		}
		sb.WriteString("\n")
	}

	if result.Omitted > 0 {
		fmt.Fprintf(&sb, "(%d card%s omitted by budget)\n", result.Omitted, plural(result.Omitted))
	}
	return sb.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// workItemIDRe matches feat-, bug-, and spk- prefixed IDs with 8 hex chars.
var workItemIDRe = regexp.MustCompile(`^(feat|bug|spk)-[0-9a-f]{8}$`)

// LooksLikeWorkItemID reports whether s is a work-item ID (feat-/bug-/spk- prefix
// followed by exactly 8 hex chars).
func LooksLikeWorkItemID(s string) bool {
	return workItemIDRe.MatchString(s)
}
