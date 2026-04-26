package slug_test

import (
	"testing"

	"github.com/shakestzd/htmlgraph/internal/slug"
)

func TestMake(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "simple lowercase",
			input:  "hello world",
			maxLen: 0,
			want:   "hello-world",
		},
		{
			name:   "uppercase converted",
			input:  "My Track Title",
			maxLen: 0,
			want:   "my-track-title",
		},
		{
			name:   "punctuation collapsed",
			input:  "Fix: Critical Bug!",
			maxLen: 0,
			want:   "fix-critical-bug",
		},
		{
			name:   "multiple spaces collapsed",
			input:  "foo   bar",
			maxLen: 0,
			want:   "foo-bar",
		},
		{
			name:   "truncate at word boundary",
			input:  "this-is-a-very-long-title-that-exceeds-the-limit",
			maxLen: 20,
			want:   "this-is-a-very-long",
		},
		{
			name:   "no truncation needed",
			input:  "short",
			maxLen: 30,
			want:   "short",
		},
		{
			name:   "exact length",
			input:  "exact",
			maxLen: 5,
			want:   "exact",
		},
		{
			name:   "trailing hyphen stripped",
			input:  "hello!",
			maxLen: 0,
			want:   "hello",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 30,
			want:   "",
		},
		{
			name:   "numbers preserved",
			input:  "version 2.0 release",
			maxLen: 0,
			want:   "version-2-0-release",
		},
		{
			name:   "leading separator stripped via no leading hyphen rule",
			input:  "!hello",
			maxLen: 0,
			want:   "hello",
		},
		{
			name:   "project basename with path",
			input:  "htmlgraph",
			maxLen: 30,
			want:   "htmlgraph",
		},
		{
			// Regression: non-ASCII letters used to be retained via
			// unicode.IsLetter and could be split across rune boundaries
			// by byte-level truncation, producing invalid UTF-8.
			name:   "non-ASCII letters dropped",
			input:  "Café Résumé",
			maxLen: 0,
			want:   "caf-r-sum",
		},
		{
			name:   "non-ASCII digits dropped",
			input:  "version ٢ release", // Arabic-Indic digit ٢
			maxLen: 0,
			want:   "version-release",
		},
		{
			name:   "cjk dropped without invalid utf8",
			input:  "feat 日本語 ship",
			maxLen: 0,
			want:   "feat-ship",
		},
		{
			// Truncation lands inside what used to be a multi-byte rune;
			// the new implementation guarantees ASCII-only output, so the
			// byte slice is always valid UTF-8.
			name:   "truncate stays utf8 valid",
			input:  "alpha beta café delta",
			maxLen: 12,
			want:   "alpha-beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slug.Make(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("Make(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestWorkItemColor(t *testing.T) {
	tests := []struct {
		typeName string
		want     string
	}{
		{"feature", "blue"},
		{"bug", "red"},
		{"spike", "purple"},
		{"track", "green"},
		{"plan", "yellow"},
		{"unknown", "blue"},
		{"", "blue"},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			got := slug.WorkItemColor(tt.typeName)
			if got != tt.want {
				t.Errorf("WorkItemColor(%q) = %q, want %q", tt.typeName, got, tt.want)
			}
		})
	}
}
