package hooks

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// writeTemp creates a temporary file with the given content and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), name)
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	f.Close()
	return f.Name()
}

// repeatLines builds a string of n identical lines each containing text.
func repeatLines(text string, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildGoFunc builds a Go function with bodyLines lines in its body.
func buildGoFunc(name string, bodyLines int) string {
	var sb strings.Builder
	sb.WriteString("func " + name + "() {\n")
	for i := 0; i < bodyLines; i++ {
		sb.WriteString("\tx := 1\n")
	}
	sb.WriteString("}\n")
	return sb.String()
}

// buildPyFunc builds a Python function with bodyLines lines in its body.
func buildPyFunc(name string, bodyLines int) string {
	var sb strings.Builder
	sb.WriteString("def " + name + "():\n")
	for i := 0; i < bodyLines; i++ {
		sb.WriteString("    x = 1\n")
	}
	return sb.String()
}

// ---- Module size tests -------------------------------------------------------

func TestCheckFileQuality_UnderLimit_NoWarning(t *testing.T) {
	// Build 200 unique lines so neither line-count nor duplication triggers.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString(fmt.Sprintf("x_%d = %d\n", i, i*7))
	}
	path := writeTemp(t, "module*.py", sb.String())
	got := CheckFileQuality(path)
	if got != "" {
		t.Errorf("expected no warnings for 200-line file, got: %q", got)
	}
}

func TestCheckFileQuality_OverWarnThreshold(t *testing.T) {
	content := repeatLines("x = 1", 350)
	path := writeTemp(t, "module*.py", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "350 lines") {
		t.Errorf("expected line count warning, got: %q", got)
	}
	if !strings.Contains(got, "target: <300") {
		t.Errorf("expected 'target: <300' in warning, got: %q", got)
	}
	if strings.Contains(got, "LIMIT") {
		t.Errorf("should not say LIMIT at 350 lines, got: %q", got)
	}
}

func TestCheckFileQuality_OverLimitThreshold(t *testing.T) {
	content := repeatLines("x = 1", 550)
	path := writeTemp(t, "module*.py", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "550 lines") {
		t.Errorf("expected line count in warning, got: %q", got)
	}
	if !strings.Contains(got, "LIMIT") {
		t.Errorf("expected LIMIT warning at 550 lines, got: %q", got)
	}
	if !strings.Contains(got, "Must split") {
		t.Errorf("expected 'Must split' in warning, got: %q", got)
	}
}

// ---- Go function length tests ------------------------------------------------

func TestCheckFileQuality_GoShortFunctions_NoWarning(t *testing.T) {
	content := buildGoFunc("ShortA", 10) + buildGoFunc("ShortB", 15)
	path := writeTemp(t, "code*.go", content)
	got := CheckFileQuality(path)
	// Should not warn about function length (may warn about module size if large).
	if strings.Contains(got, "Function") {
		t.Errorf("expected no function length warning, got: %q", got)
	}
}

func TestCheckFileQuality_GoLongFunction_WarnThreshold(t *testing.T) {
	// 40-line body → > 30 warn threshold, < 50 limit
	content := buildGoFunc("MediumFunc", 40)
	path := writeTemp(t, "code*.go", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "MediumFunc") {
		t.Errorf("expected function name in warning, got: %q", got)
	}
	if strings.Contains(got, "Must refactor") {
		t.Errorf("should not say 'Must refactor' below limit, got: %q", got)
	}
}

func TestCheckFileQuality_GoLongFunction_LimitExceeded(t *testing.T) {
	// 60-line body → > 50 limit
	content := buildGoFunc("HugeFunc", 60)
	path := writeTemp(t, "code*.go", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "HugeFunc") {
		t.Errorf("expected function name in warning, got: %q", got)
	}
	if !strings.Contains(got, "Must refactor") {
		t.Errorf("expected 'Must refactor' for function over limit, got: %q", got)
	}
	if !strings.Contains(got, "LIMIT") {
		t.Errorf("expected LIMIT in warning, got: %q", got)
	}
}

// ---- Python function length tests -------------------------------------------

func TestCheckFileQuality_PythonShortFunction_NoWarning(t *testing.T) {
	content := buildPyFunc("small_fn", 15)
	path := writeTemp(t, "mod*.py", content)
	got := CheckFileQuality(path)
	if strings.Contains(got, "Function") {
		t.Errorf("expected no function length warning, got: %q", got)
	}
}

func TestCheckFileQuality_PythonLongFunction_LimitExceeded(t *testing.T) {
	// 55-line body → > 50 limit
	content := buildPyFunc("big_fn", 55)
	path := writeTemp(t, "mod*.py", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "big_fn") {
		t.Errorf("expected function name in warning, got: %q", got)
	}
	if !strings.Contains(got, "Must refactor") {
		t.Errorf("expected 'Must refactor' for Python function over limit, got: %q", got)
	}
}

// ---- Duplication tests -------------------------------------------------------

func TestCheckFileQuality_DuplicateBlocks_Warned(t *testing.T) {
	// Build a 6-line block and repeat it 3 times with unique content around it.
	block := "line_a\nline_b\nline_c\nline_d\nline_e\nline_f\n"
	content := "preamble1\n" + block + "middle\n" + block + "end\n" + block
	path := writeTemp(t, "dup*.py", content)
	got := CheckFileQuality(path)
	if !strings.Contains(got, "duplicate block") {
		t.Errorf("expected duplicate block warning, got: %q", got)
	}
}

func TestCheckFileQuality_NoDuplicates_NoWarning(t *testing.T) {
	// Each line is unique (uses decimal i), so no 5-line block repeats.
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(fmt.Sprintf("unique_line_number_%d\n", i))
	}
	path := writeTemp(t, "unique*.py", sb.String())
	got := CheckFileQuality(path)
	if strings.Contains(got, "duplicate") {
		t.Errorf("expected no duplication warning, got: %q", got)
	}
}

// ---- Non-code file tests -----------------------------------------------------

func TestCheckFileQuality_MarkdownFile_OnlyLineCount(t *testing.T) {
	content := repeatLines("Some markdown text here.", 350)
	path := writeTemp(t, "README*.md", content)
	got := CheckFileQuality(path)
	// Should warn about line count (350 > 300) but NOT about functions.
	if strings.Contains(got, "Function") {
		t.Errorf("markdown should not trigger function check, got: %q", got)
	}
	if !strings.Contains(got, "lines") {
		t.Errorf("markdown over 300 lines should still get line count warning, got: %q", got)
	}
}

func TestCheckFileQuality_JSONFile_OnlyLineCount(t *testing.T) {
	content := repeatLines(`  "key": "value",`, 350)
	path := writeTemp(t, "config*.json", content)
	got := CheckFileQuality(path)
	if strings.Contains(got, "Function") {
		t.Errorf("JSON should not trigger function check, got: %q", got)
	}
}

// ---- Binary / skip tests ----------------------------------------------------

func TestCheckFileQuality_PNGFile_Skipped(t *testing.T) {
	path := writeTemp(t, "image*.png", repeatLines("garbage", 600))
	got := CheckFileQuality(path)
	if got != "" {
		t.Errorf("binary file should be skipped entirely, got: %q", got)
	}
}

func TestCheckFileQuality_wipnoteDir_Skipped(t *testing.T) {
	// Simulate a path inside .wipnote/ — we don't need it to actually exist.
	got := CheckFileQuality(".wipnote/features/feat-abc.html")
	if got != "" {
		t.Errorf(".wipnote/ paths should be skipped, got: %q", got)
	}
}

func TestCheckFileQuality_MissingFile_NoError(t *testing.T) {
	got := CheckFileQuality("/nonexistent/path/to/file.go")
	if got != "" {
		t.Errorf("missing file should return empty string, got: %q", got)
	}
}

// ---- extractFilePath tests ---------------------------------------------------

func TestExtractFilePath_FilePathKey(t *testing.T) {
	input := map[string]any{"file_path": "/tmp/foo.go"}
	got := extractFilePath(input)
	if got != "/tmp/foo.go" {
		t.Errorf("got %q, want /tmp/foo.go", got)
	}
}

func TestExtractFilePath_PathKey(t *testing.T) {
	input := map[string]any{"path": "/tmp/bar.py"}
	got := extractFilePath(input)
	if got != "/tmp/bar.py" {
		t.Errorf("got %q, want /tmp/bar.py", got)
	}
}

func TestExtractFilePath_NoKeys_Empty(t *testing.T) {
	input := map[string]any{"content": "hello"}
	got := extractFilePath(input)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
