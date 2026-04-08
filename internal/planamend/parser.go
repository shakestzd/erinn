// Package planamend parses AMEND directives from chat text.
//
// Syntax:
//
//	AMEND slice-N: <operation> <field> "<content>"
//	AMEND slice-N: <operation> <field> `content`
//	AMEND slice-N: <operation> <field> content-to-end-of-line
//
// Operations: add, remove, set
// Fields: done_when, files, title, what, why, effort, risk
package planamend

import (
	"regexp"
	"strconv"
	"strings"
)

// Amendment represents a parsed AMEND directive from chat text.
type Amendment struct {
	SliceNum  int    `json:"slice_num"`
	Operation string `json:"operation"` // add, remove, set
	Field     string `json:"field"`     // done_when, files, title, what, why, effort, risk
	Content   string `json:"content"`
}

// amendRE matches AMEND directives in the form:
//
//	AMEND slice-N: operation field "quoted" | `backtick` | bare
//
// Groups:
//  1. slice number
//  2. operation (add|remove|set)
//  3. field name
//  4. double-quoted content (without quotes)
//  5. backtick-quoted content (without backticks)
//  6. bare content (to end of line)
var amendRE = regexp.MustCompile(
	`(?im)AMEND\s+slice-(\d+)\s*:\s*` +
		`(add|remove|set)\s+` +
		`(done_when|files|title|what|why|effort|risk)\s+` +
		`(?:"([^"]+)"|` + "`" + `([^` + "`" + `]+)` + "`" + `|(.+?))\s*$`,
)

// ParseAmendments extracts AMEND directives from text and returns them in
// order of appearance. Returns nil when no directives are found.
func ParseAmendments(text string) []Amendment {
	matches := amendRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	results := make([]Amendment, 0, len(matches))
	for _, m := range matches {
		num, _ := strconv.Atoi(m[1])
		content := m[4]
		if content == "" {
			content = m[5]
		}
		if content == "" {
			content = m[6]
		}
		results = append(results, Amendment{
			SliceNum:  num,
			Operation: strings.ToLower(m[2]),
			Field:     strings.ToLower(m[3]),
			Content:   strings.TrimSpace(content),
		})
	}
	return results
}
