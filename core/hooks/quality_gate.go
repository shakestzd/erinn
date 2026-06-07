package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// binaryExtensions lists file extensions we skip entirely.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".wasm": true, ".bin": true, ".exe": true, ".so": true, ".dylib": true,
	".dll": true, ".pdf": true, ".zip": true, ".tar": true, ".gz": true,
}

// funcPrefixes maps file extension to function declaration prefix.
var funcPrefixes = map[string]string{
	".go":  "func ",
	".py":  "def ",
	".ts":  "function ",
	".js":  "function ",
	".tsx": "function ",
	".jsx": "function ",
}

// lineCountLimits defines (warning, fail) thresholds for module size.
// See constants.go for constant definitions.

// CheckFileQuality reads the file that was just written and returns advisory
// warnings if it violates size/complexity limits. Returns empty string when
// no issues are found. Designed to complete in <5ms (single read, line scan).
func CheckFileQuality(filePath string) string {
	// Skip .wipnote/ directory entirely.
	if containsWipnoteDir(filePath) {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(filePath))

	// Skip binary files.
	if binaryExtensions[ext] {
		return ""
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}

	lines := splitLines(data)
	shortName := filepath.Base(filePath)

	var warnings []string

	// Module size check.
	if w := checkModuleSize(shortName, len(lines)); w != "" {
		warnings = append(warnings, w)
	}

	// Function length check (language-aware).
	if prefix, ok := funcPrefixes[ext]; ok {
		warnings = append(warnings, checkFunctionLengths(shortName, lines, prefix)...)
	}

	// Duplication check.
	if w := checkDuplication(shortName, lines); w != "" {
		warnings = append(warnings, w)
	}

	return strings.Join(warnings, "\n")
}

// checkModuleSize returns a warning string when lineCount exceeds thresholds.
func checkModuleSize(name string, lineCount int) string {
	switch {
	case lineCount > moduleLimitLines:
		return fmt.Sprintf("[Quality] %s is %d lines (LIMIT: %d). Must split before committing.",
			name, lineCount, moduleLimitLines)
	case lineCount > moduleWarnLines:
		return fmt.Sprintf("[Quality] %s is %d lines (target: <%d). Consider splitting.",
			name, lineCount, moduleWarnLines)
	}
	return ""
}

// checkFunctionLengths scans lines for function declarations using funcPrefix
// and warns when any function exceeds the length thresholds.
func checkFunctionLengths(fileName string, lines []string, funcPrefix string) []string {
	var warnings []string

	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, funcPrefix) {
			continue
		}

		funcName := extractFuncName(trimmed, funcPrefix)
		length := measureFuncLength(lines, i, line)

		switch {
		case length > funcLimitLines:
			warnings = append(warnings, fmt.Sprintf(
				"[Quality] Function %s in %s is %d lines (LIMIT: %d). Must refactor.",
				funcName, fileName, length, funcLimitLines))
		case length > funcWarnLines:
			warnings = append(warnings, fmt.Sprintf(
				"[Quality] Function %s in %s is %d lines (target: <%d).",
				funcName, fileName, length, funcWarnLines))
		}
	}

	return warnings
}

// measureFuncLength counts lines from the declaration until the closing brace
// at the same or lower indentation level (handles Go, Python by indent depth).
func measureFuncLength(lines []string, startIdx int, declarationLine string) int {
	baseIndent := indentDepth(declarationLine)
	count := 1
	openBraces := 0

	// Count opening braces on the declaration line itself.
	for _, ch := range lines[startIdx] {
		switch ch {
		case '{':
			openBraces++
		case '}':
			openBraces--
		}
	}

	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		count++

		for _, ch := range line {
			switch ch {
			case '{':
				openBraces++
			case '}':
				openBraces--
			}
		}

		// Brace-based termination (Go/JS/TS).
		if openBraces <= 0 && strings.Contains(lines[startIdx], "{") {
			return count
		}

		// Indent-based termination (Python): when we hit a non-empty line
		// at or below base indentation after at least one body line.
		if count > 1 && line != "" && !isBlankOrComment(line) {
			if indentDepth(line) <= baseIndent && !strings.Contains(lines[startIdx], "{") {
				return count - 1
			}
		}
	}

	return count
}

// checkDuplication looks for blocks of dupBlockSize consecutive lines that:
//   - contain at least 2 distinct line values (not pure repetition of one line), and
//   - appear at more than one non-overlapping position in the file.
//
// This catches genuine copy-paste patterns while ignoring boilerplate like
// repeated single-line patterns (e.g. 200 lines of "x = 1").
func checkDuplication(fileName string, lines []string) string {
	if len(lines) < dupBlockSize*2 {
		return ""
	}

	// Phase 1: build fingerprint → first occurrence index map (sliding window).
	type occurrence struct{ count, firstIdx int }
	seen := make(map[string]*occurrence)
	for i := 0; i+dupBlockSize <= len(lines); i++ {
		block := lines[i : i+dupBlockSize]
		// Skip trivial blocks (all blank or all identical lines).
		if strings.TrimSpace(strings.Join(block, "\n")) == "" {
			continue
		}
		if allSameLine(block) {
			continue
		}
		key := strings.Join(block, "\n")
		if oc, ok := seen[key]; ok {
			// Only count as a new occurrence if non-overlapping with previous.
			if i >= oc.firstIdx+dupBlockSize {
				oc.count++
				oc.firstIdx = i
			}
		} else {
			seen[key] = &occurrence{count: 1, firstIdx: i}
		}
	}

	duplicateBlocks := 0
	for _, oc := range seen {
		if oc.count > 1 {
			duplicateBlocks++
		}
	}

	if duplicateBlocks > 0 {
		return fmt.Sprintf(
			"[Quality] %s has %d duplicate block(s) (%d lines each). Extract shared helper.",
			fileName, duplicateBlocks, dupBlockSize)
	}
	return ""
}

// allSameLine returns true when every line in the slice is identical,
// which indicates a trivial repetition (separator, boilerplate token)
// rather than a meaningful copy-paste.
func allSameLine(lines []string) bool {
	if len(lines) == 0 {
		return true
	}
	first := lines[0]
	for _, l := range lines[1:] {
		if l != first {
			return false
		}
	}
	return true
}

// extractFilePath returns the file path from Write/Edit/MultiEdit tool input.
func extractFilePath(input map[string]any) string {
	// MultiEdit: extract the first file path from the edits array.
	if edits, ok := input["edits"].([]any); ok && len(edits) > 0 {
		if edit, ok := edits[0].(map[string]any); ok {
			if fp, ok := edit["file_path"].(string); ok && fp != "" {
				return fp
			}
		}
	}
	// Standard path for Write/Edit.
	for _, key := range []string{"file_path", "path"} {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// --- helpers ----------------------------------------------------------------

// splitLines splits raw bytes into lines (strips trailing \r).
func splitLines(data []byte) []string {
	raw := strings.Split(string(data), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	// Trim trailing empty line added by final newline.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// indentDepth counts leading spaces/tabs (tabs count as 1).
func indentDepth(line string) int {
	for i, ch := range line {
		if ch != ' ' && ch != '\t' {
			return i
		}
	}
	return len(line)
}

// extractFuncName extracts the function name from a declaration line.
// e.g. "func Foo(x int) string {" → "Foo"
// e.g. "def bar(self):" → "bar"
func extractFuncName(trimmed, prefix string) string {
	after := trimmed[len(prefix):]
	for i, ch := range after {
		if ch == '(' || ch == ' ' || ch == ':' {
			return after[:i]
		}
	}
	if len(after) > 40 {
		return after[:40]
	}
	return after
}

// isBlankOrComment returns true for empty lines or comment-only lines.
func isBlankOrComment(line string) bool {
	t := strings.TrimLeft(line, " \t")
	return t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")
}
