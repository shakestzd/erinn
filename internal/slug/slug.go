// Package slug provides shared helpers for generating URL/TUI-safe slugs
// from work item titles and for mapping item types to Claude session colors.
package slug

import (
	"strings"
)

// WorkItemColor returns the Claude session color for a given work item type.
// The 8 valid Claude colors are: red, blue, green, yellow, purple, orange, pink, cyan.
func WorkItemColor(typeName string) string {
	switch typeName {
	case "feature":
		return "blue"
	case "bug":
		return "red"
	case "spike":
		return "purple"
	case "track":
		return "green"
	case "plan":
		return "yellow"
	default:
		return "blue"
	}
}

// Make converts a string to a URL/TUI-safe slug:
//   - Lowercase
//   - ASCII alphanumerics and hyphens only — non-ASCII characters
//     (accented letters, CJK, emoji, etc.) are treated as separators so
//     byte-level truncation cannot split a multi-byte rune and produce
//     invalid UTF-8. Slugs feed filenames and URLs, where non-ASCII is
//     a portability hazard regardless.
//   - Runs of non-alphanumeric characters collapsed to a single hyphen
//   - Leading and trailing hyphens stripped
//   - Capped at maxLen bytes with word-boundary truncation
//
// Pass maxLen == 0 to skip truncation.
func Make(s string, maxLen int) string {
	if s == "" {
		return ""
	}

	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		// Collapse any run of separators (spaces, punctuation, non-ASCII
		// runes, etc.) to a single hyphen.
		if !prevHyphen && b.Len() > 0 {
			b.WriteRune('-')
			prevHyphen = true
		}
	}

	slug := strings.TrimRight(b.String(), "-")

	// All retained characters are single-byte ASCII, so byte slicing is
	// rune-safe; len(slug) and maxLen compare in bytes == characters.
	if maxLen <= 0 || len(slug) <= maxLen {
		return slug
	}
	truncated := slug[:maxLen]
	if idx := strings.LastIndex(truncated, "-"); idx > 0 {
		truncated = truncated[:idx]
	}
	return strings.TrimRight(truncated, "-")
}
