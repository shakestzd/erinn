package hooks

import "testing"

func TestSummariseReadInput(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "path only",
			input: map[string]any{"file_path": "/src/main.go"},
			want:  "/src/main.go",
		},
		{
			name:  "path with offset and limit",
			input: map[string]any{"file_path": "/src/main.go", "offset": float64(100), "limit": float64(50)},
			want:  "/src/main.go [100:150]",
		},
		{
			name:  "path with offset only",
			input: map[string]any{"file_path": "/src/main.go", "offset": float64(100)},
			want:  "/src/main.go [100:]",
		},
		{
			name:  "path with limit only",
			input: map[string]any{"file_path": "/src/main.go", "limit": float64(50)},
			want:  "/src/main.go [:50]",
		},
		{
			name:  "no path returns Read",
			input: map[string]any{},
			want:  "Read",
		},
		{
			name:  "nil input returns Read",
			input: nil,
			want:  "Read",
		},
		{
			name:  "zero offset and limit omits range",
			input: map[string]any{"file_path": "/src/main.go", "offset": float64(0), "limit": float64(0)},
			want:  "/src/main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summariseReadInput(tt.input)
			if got != tt.want {
				t.Errorf("summariseReadInput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummariseInputReadDispatch(t *testing.T) {
	// Verify SummariseInput dispatches to summariseReadInput for Read tool.
	input := map[string]any{"file_path": "/foo.go", "offset": float64(10), "limit": float64(20)}
	got := SummariseInput("Read", input)
	want := "/foo.go [10:30]"
	if got != want {
		t.Errorf("SummariseInput(Read) = %q, want %q", got, want)
	}
}

func TestSummariseInputNonRead(t *testing.T) {
	// Non-Read tools should use the old path-only logic.
	input := map[string]any{"file_path": "/foo.go", "offset": float64(10)}
	got := SummariseInput("Write", input)
	want := "/foo.go"
	if got != want {
		t.Errorf("SummariseInput(Write) = %q, want %q", got, want)
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want int
	}{
		{"float64", float64(42), 42},
		{"int", int(7), 7},
		{"int64", int64(99), 99},
		{"string", "10", 0},
		{"nil", nil, 0},
		{"bool", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toInt(tt.val)
			if got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.val, got, tt.want)
			}
		})
	}
}

func TestSummariseToolOutput(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		input   map[string]any
		result  map[string]any
		success bool
		want    string
	}{
		{
			name:    "Read success with content",
			tool:    "Read",
			input:   map[string]any{"file_path": "/src/main.go"},
			result:  map[string]any{"content": "line1\nline2\nline3\n"},
			success: true,
			want:    "/src/main.go (ok, 3 lines)",
		},
		{
			name:    "Read error",
			tool:    "Read",
			input:   map[string]any{"file_path": "/missing.go"},
			result:  map[string]any{"error": "file not found"},
			success: false,
			want:    "/missing.go (error)",
		},
		{
			name:    "Write success",
			tool:    "Write",
			input:   map[string]any{"file_path": "/out.go"},
			result:  map[string]any{},
			success: true,
			want:    "/out.go (written)",
		},
		{
			name:    "Edit success",
			tool:    "Edit",
			input:   map[string]any{"file_path": "/out.go"},
			result:  map[string]any{},
			success: true,
			want:    "/out.go (edited)",
		},
		{
			name:    "Glob success",
			tool:    "Glob",
			input:   map[string]any{"pattern": "*.go"},
			result:  map[string]any{"output": "a.go\nb.go\nc.go\n"},
			success: true,
			want:    "3 files matched",
		},
		{
			name:    "Grep success",
			tool:    "Grep",
			input:   map[string]any{"pattern": "foo"},
			result:  map[string]any{"output": "file1.go:10:foo\nfile2.go:20:foo\n"},
			success: true,
			want:    "2 matches",
		},
		{
			name:    "Unknown tool falls back to summariseOutput",
			tool:    "Bash",
			input:   map[string]any{"command": "echo hi"},
			result:  map[string]any{"output": "hi"},
			success: true,
			want:    "hi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summariseToolOutput(tt.tool, tt.input, tt.result, tt.success)
			if got != tt.want {
				t.Errorf("summariseToolOutput(%s) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"single line no newline", "hello", 1},
		{"single line with newline", "hello\n", 1},
		{"two lines", "a\nb\n", 2},
		{"three lines no trailing", "a\nb\nc", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countLines(tt.s)
			if got != tt.want {
				t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}
