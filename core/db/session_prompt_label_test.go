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
		{
			// Regression (roborev job 446): ordinary prompt text containing
			// JSX/XML/HTML must NOT be treated as injected markup. Only the
			// brackets-with-content of KNOWN injected tags are stripped; a
			// <Button>…</Button> snippet is preserved verbatim.
			name:  "jsx_snippet_in_user_prompt_preserved",
			input: "Fix <Button>Save</Button> alignment",
			want:  "Fix <Button>Save</Button> alignment",
		},
		{
			name:  "unknown_paired_tag_content_preserved",
			input: "render <Foo>bar</Foo> please",
			want:  "render <Foo>bar</Foo> please",
		},
		{
			// Regression (roborev job 449): generic HTML tag names that are ALSO
			// injected-metadata names (summary/status/note/result) must be
			// preserved in ordinary user prompts — they are only stripped when
			// nested inside an injected wrapper block.
			name:  "generic_html_summary_tag_preserved",
			input: "add a <summary>View Plan YAML</summary> toggle",
			want:  "add a <summary>View Plan YAML</summary> toggle",
		},
		{
			name:  "status_note_result_tags_preserved_standalone",
			input: "show <status>ok</status> and <result>42</result>",
			want:  "show <status>ok</status> and <result>42</result>",
		},
		{
			// A real injected wrapper block (with nested generic tags) IS stripped
			// wholesale, including the nested <status>/<summary>/<result>.
			name:  "wrapper_block_with_nested_generic_tags_stripped",
			input: "do X <task-notification><task-id>z</task-id><status>completed</status><summary>done</summary></task-notification> now",
			want:  "do X now",
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
