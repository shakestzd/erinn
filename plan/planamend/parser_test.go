package planamend

import "testing"

func TestParseAmendments(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []Amendment
	}{
		{
			name:     "quoted content",
			input:    `AMEND slice-1: add done_when "Complete unit tests"`,
			expected: []Amendment{{SliceNum: 1, Operation: "add", Field: "done_when", Content: "Complete unit tests"}},
		},
		{
			name:     "backtick content",
			input:    "AMEND slice-2: set effort `M`",
			expected: []Amendment{{SliceNum: 2, Operation: "set", Field: "effort", Content: "M"}},
		},
		{
			name:     "bare content",
			input:    "AMEND slice-3: remove files cmd/old.go",
			expected: []Amendment{{SliceNum: 3, Operation: "remove", Field: "files", Content: "cmd/old.go"}},
		},
		{
			name:     "case insensitive",
			input:    `amend SLICE-1: ADD done_when "test"`,
			expected: []Amendment{{SliceNum: 1, Operation: "add", Field: "done_when", Content: "test"}},
		},
		{
			name:  "multiple amendments",
			input: "Some text\nAMEND slice-1: add files \"new.go\"\nMore text\nAMEND slice-2: set risk \"High\"",
			expected: []Amendment{
				{SliceNum: 1, Operation: "add", Field: "files", Content: "new.go"},
				{SliceNum: 2, Operation: "set", Field: "risk", Content: "High"},
			},
		},
		{
			name:     "no amendments",
			input:    "This is regular text with no directives",
			expected: nil,
		},
		{
			name:     "malformed — missing slice number",
			input:    "AMEND slice-: add files foo",
			expected: nil,
		},
		{
			name:     "invalid field ignored",
			input:    "AMEND slice-1: add unknown_field value",
			expected: nil,
		},
		{
			name:     "invalid operation ignored",
			input:    `AMEND slice-1: modify files "something"`,
			expected: nil,
		},
		{
			name:  "all valid operations",
			input: "AMEND slice-1: add files a.go\nAMEND slice-2: remove files b.go\nAMEND slice-3: set title New Title",
			expected: []Amendment{
				{SliceNum: 1, Operation: "add", Field: "files", Content: "a.go"},
				{SliceNum: 2, Operation: "remove", Field: "files", Content: "b.go"},
				{SliceNum: 3, Operation: "set", Field: "title", Content: "New Title"},
			},
		},
		{
			name:  "all valid fields",
			input: "AMEND slice-1: set done_when done\nAMEND slice-1: set files f.go\nAMEND slice-1: set title T\nAMEND slice-1: set what W\nAMEND slice-1: set why Y\nAMEND slice-1: set effort S\nAMEND slice-1: set risk Low",
			expected: []Amendment{
				{SliceNum: 1, Operation: "set", Field: "done_when", Content: "done"},
				{SliceNum: 1, Operation: "set", Field: "files", Content: "f.go"},
				{SliceNum: 1, Operation: "set", Field: "title", Content: "T"},
				{SliceNum: 1, Operation: "set", Field: "what", Content: "W"},
				{SliceNum: 1, Operation: "set", Field: "why", Content: "Y"},
				{SliceNum: 1, Operation: "set", Field: "effort", Content: "S"},
				{SliceNum: 1, Operation: "set", Field: "risk", Content: "Low"},
			},
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAmendments(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d amendments, got %d: %+v", len(tt.expected), len(got), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("amendment %d: expected %+v, got %+v", i, tt.expected[i], got[i])
				}
			}
		})
	}
}
