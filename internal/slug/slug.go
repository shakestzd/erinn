// Package slug provides shared helpers for generating URL/TUI-safe slugs
// from work item titles and for mapping item types to Claude session colors.
package slug

import (
	"strings"
	"unicode"
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
//   - Alphanumerics and hyphens only
//   - Runs of non-alphanumeric characters collapsed to a single hyphen
//   - Leading and trailing hyphens stripped
//   - Capped at maxLen characters with word-boundary truncation
//
// Pass maxLen == 0 to skip truncation.
func Make(s string, maxLen int) string {
	if s == "" {
		return ""
	}

	// Build slug character by character.
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else {
			// Collapse any run of separators (spaces, punctuation, etc.) to one hyphen.
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}

	slug := strings.TrimRight(b.String(), "-")

	if maxLen <= 0 || len(slug) <= maxLen {
		return slug
	}

	// Truncate at a word boundary (hyphen) within maxLen.
	truncated := slug[:maxLen]
	if idx := strings.LastIndex(truncated, "-"); idx > 0 {
		truncated = truncated[:idx]
	}
	return strings.TrimRight(truncated, "-")
}
