package db

import "testing"

// TestSanitizePromptLabel covers the sanitizePromptLabel helper which is used
// by sessionPromptLabel to strip injected markup and collapse whitespace.
func TestSanitizePromptLabel(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "task_notification_tags_and_newlines",
			input: "Resume this session  de22a960 · <task-notification>\n" +
				"<task-id>a34a74dbc93d6a198</task-id>\n" +
				"<tool-use-id>toolu_xyz</tool-use-id>\n" +
				"· Claude · branch main · 9h ago",
			want: "Resume this session de22a960 · · Claude · branch main · 9h ago",
		},
		{
			name:  "normal_prompt_unchanged",
			input: "Fix the broken login flow",
			want:  "Fix the broken login flow",
		},
		{
			name:  "entirely_tags_returns_empty",
			input: "<task-notification><task-id>abc</task-id><tool-use-id>xyz</tool-use-id>",
			want:  "",
		},
		{
			name:  "embedded_newlines_and_tabs_become_single_space",
			input: "first line\nsecond line\t\tthird",
			want:  "first line second line third",
		},
		{
			name:  "leading_and_trailing_whitespace_trimmed",
			input: "   hello world   ",
			want:  "hello world",
		},
		{
			name:  "empty_string_returns_empty",
			input: "",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizePromptLabel(tc.input)
			if got != tc.want {
				t.Errorf("sanitizePromptLabel(%q)\n got  %q\n want %q", tc.input, got, tc.want)
			}
		})
	}
}
