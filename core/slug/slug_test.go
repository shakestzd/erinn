package slug_test

import (
	"testing"
	"unicode/utf8"

	"github.com/shakestzd/wipnote/core/slug"
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
			input:  "wipnote",
			maxLen: 30,
			want:   "wipnote",
		},
		{
			name:   "non-ASCII title stripped to ASCII words only",
			input:  "日本語タイトル foo bar baz",
			maxLen: 20,
			want:   "foo-bar-baz",
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

// TestMakeNonASCIIValidUTF8 verifies that a title containing non-ASCII runes
// produces a slug that is valid UTF-8 and does not exceed the byte cap.
func TestMakeNonASCIIValidUTF8(t *testing.T) {
	const maxLen = 20
	result := slug.Make("日本語タイトル foo bar baz", maxLen)
	if !utf8.ValidString(result) {
		t.Errorf("Make returned invalid UTF-8: %q", result)
	}
	if len(result) > maxLen {
		t.Errorf("Make result %q exceeds maxLen %d (got %d bytes)", result, maxLen, len(result))
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
