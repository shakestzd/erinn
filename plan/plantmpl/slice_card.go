package plantmpl

import (
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

// mdStripRe matches common inline Markdown formatting tokens for stripping.
var mdStripRe = regexp.MustCompile(`(\*\*|__|[*_]|` + "`" + `+|\[([^\]]*)\]\([^)]*\))`)

// htmlTagRe matches HTML tags (opening, closing, self-closing) for removal.
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// stripMarkdown removes inline Markdown formatting and raw HTML tags from src
// and returns plain text suitable for a one-line preview. It:
//   - strips HTML tags (including any embedded event handlers like onerror=)
//   - removes bold/italic markers (**,__,*,_)
//   - removes inline code backticks (keeps inner text)
//   - converts links [text](url) → text
//   - strips leading list/heading markers (-, *, #, >, digits.)
func stripMarkdown(src string) string {
	s := strings.TrimSpace(src)
	// Strip HTML tags first — this removes any raw HTML including event handlers.
	s = htmlTagRe.ReplaceAllString(s, "")
	// Strip leading list/heading/blockquote markers on the first line only
	s = regexp.MustCompile(`^[\s]*[#>\-*\d.]+\s*`).ReplaceAllStringFunc(s, func(m string) string {
		// Only strip if the entire leading portion is markup
		return ""
	})
	// Replace [text](url) links with just text
	s = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(s, "$1")
	// Remove backtick sequences (inline code — keep inner text)
	s = regexp.MustCompile("`+([^`]*)`+").ReplaceAllString(s, "$1")
	// Remove bold/italic markers
	s = regexp.MustCompile(`(\*\*|__|[*_])`).ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// previewMaxRunes is the maximum rune count for a slice summary preview.
const previewMaxRunes = 140

// Preview returns a short plain-text preview of the slice content: the first
// sentence (or line) of the What field, stripped of Markdown formatting, trimmed
// to previewMaxRunes. Falls back to Description for legacy slices. Returns empty
// string when neither field has content.
func (sc *SliceCard) Preview() string {
	src := sc.What
	if strings.TrimSpace(src) == "" {
		src = sc.Description
	}
	if strings.TrimSpace(src) == "" {
		return ""
	}
	// Take the first non-empty line as the candidate sentence.
	first := ""
	for line := range strings.SplitSeq(src, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			first = t
			break
		}
	}
	if first == "" {
		return ""
	}
	// Also split on ". " to get the first sentence if the line is long.
	if idx := strings.Index(first, ". "); idx > 0 && idx < previewMaxRunes {
		first = first[:idx+1]
	}
	plain := stripMarkdown(first)
	if utf8.RuneCountInString(plain) > previewMaxRunes {
		runes := []rune(plain)
		plain = string(runes[:previewMaxRunes]) + "…"
	}
	return plain
}

var sliceCardTmpl = template.Must(
	template.New("slice_card.gohtml").Funcs(sliceCardFuncs).ParseFS(templateFS, "templates/slice_card.gohtml"),
)

// revealThreshold is the character count above which a markdown block gets a
// "Show full description" reveal toggle.
const revealThreshold = 400

// TestGroup holds parsed test strategy items grouped by prefix (Unit, Integration, etc).
type TestGroup struct {
	Kind  string
	Items []string
}

// ParseTestGroups parses a test strategy string into typed groups.
// Lines matching "Kind: item" are grouped; if no recognisable prefix is found
// on any line, nil is returned and the caller falls back to prose rendering.
func ParseTestGroups(tests string) []TestGroup {
	if strings.TrimSpace(tests) == "" {
		return nil
	}
	lines := strings.Split(tests, "\n")
	var groups []TestGroup
	groupIdx := map[string]int{}
	anyParsed := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon > 0 && colon < 25 {
			kind := strings.TrimSpace(line[:colon])
			// Validate: kind should be 1-4 words, no spaces is common (Unit, Integration)
			if !strings.ContainsAny(kind, ".,;()[]{}") && len(strings.Fields(kind)) <= 3 {
				item := strings.TrimSpace(line[colon+1:])
				kindLower := strings.ToLower(kind)
				if idx, ok := groupIdx[kindLower]; ok {
					groups[idx].Items = append(groups[idx].Items, item)
				} else {
					groupIdx[kindLower] = len(groups)
					groups = append(groups, TestGroup{Kind: kind, Items: []string{item}})
				}
				anyParsed = true
				continue
			}
		}
		// Unparseable line — add to a "Other" group
		kindLower := "other"
		if idx, ok := groupIdx[kindLower]; ok {
			groups[idx].Items = append(groups[idx].Items, line)
		} else {
			groupIdx[kindLower] = len(groups)
			groups = append(groups, TestGroup{Kind: "Other", Items: []string{line}})
		}
	}
	if !anyParsed {
		return nil
	}
	return groups
}

// SliceCard renders a single implementation slice with its metadata,
// dependencies, and approval status.
type SliceCard struct {
	Num         int
	ID          string // YAML slice id like "auth-init" (displayed in slice card)
	FeatureID   string // generated feature ID like "feat-abc123" (for Related Features lookup)
	Title       string
	Description string   // Legacy: flat description text (used when What is empty)
	What        string   // Structured: what to implement (Markdown source)
	Why         string   // Structured: rationale / motivation (Markdown source)
	DoneWhen    []string // Structured: acceptance criteria bullets (literal, no Markdown)
	Tests       string   // Test strategy text (Markdown source)
	Effort      string   // "S", "M", "L"
	Risk        string   // "Low", "Med", "High"
	Complexity  string   // "trivial" | "standard" | "complex" (empty = legacy plan, render no badge)
	Deps        string   // comma-separated slice numbers
	Files       string   // comma-separated file paths
	Status      string

	// V2 lifecycle fields (additive — legacy plans omit these and remain valid).
	ApprovalStatus  string // pending | approved | rejected | changes_requested
	ExecutionStatus string // not_started | promoted | in_progress | done | blocked | superseded

	// V2 slice-local spec fields.
	Questions       []planyaml.SliceQuestion  // slice-local open questions
	CriticRevisions []planyaml.CriticRevision // critic feedback specific to this slice
	DecisionsNotes  string                    // free-text Markdown captured by elicit-decisions
}

// IssueCount returns the number of critic revisions (issues) for this slice.
func (sc *SliceCard) IssueCount() int { return len(sc.CriticRevisions) }

// QuestionCount returns the number of open questions for this slice.
func (sc *SliceCard) QuestionCount() int { return len(sc.Questions) }

// WhatNeedsReveal returns true when the What content is long enough to warrant
// a "Show full description" toggle (character count > revealThreshold).
func (sc *SliceCard) WhatNeedsReveal() bool { return len(sc.What) > revealThreshold }

// WhyNeedsReveal returns true when the Why content is long enough to warrant
// a "Show full description" toggle.
func (sc *SliceCard) WhyNeedsReveal() bool { return len(sc.Why) > revealThreshold }

// DescriptionNeedsReveal returns true when the Description content is long enough.
func (sc *SliceCard) DescriptionNeedsReveal() bool { return len(sc.Description) > revealThreshold }

// TestGroups parses the Tests field into typed groups. Returns nil when no
// prefix pattern is detected (caller renders prose fallback).
func (sc *SliceCard) TestGroups() []TestGroup { return ParseTestGroups(sc.Tests) }

// ApprovalDataAttr returns the data-approval attribute value for CSS status stripe.
func (sc *SliceCard) ApprovalDataAttr() string {
	if sc.ApprovalStatus == "" {
		return "pending"
	}
	return sc.ApprovalStatus
}

// DoneWhenMultiCol returns true when Done When has 6 or more items (use 2-col grid).
func (sc *SliceCard) DoneWhenMultiCol() bool { return len(sc.DoneWhen) >= 6 }

// HasStructuredContent returns true when the slice has What/Why fields
// (benchmark format) rather than just a flat description.
func (sc *SliceCard) HasStructuredContent() bool {
	return sc.What != "" || sc.Why != ""
}

// WhatHTML returns the What field rendered as sanitized HTML.
func (sc *SliceCard) WhatHTML() template.HTML { return RenderMd(sc.What) }

// WhyHTML returns the Why field rendered as sanitized HTML.
func (sc *SliceCard) WhyHTML() template.HTML { return RenderMd(sc.Why) }

// DescriptionHTML returns the Description field rendered as sanitized HTML.
func (sc *SliceCard) DescriptionHTML() template.HTML { return RenderMd(sc.Description) }

// TestsHTML returns the Tests field rendered as sanitized HTML.
func (sc *SliceCard) TestsHTML() template.HTML { return RenderMd(sc.Tests) }

// Render writes the slice card HTML.
func (sc *SliceCard) Render(w io.Writer) error {
	return sliceCardTmpl.Execute(w, sc)
}

// EffortClass returns the CSS class for the effort badge.
func (sc *SliceCard) EffortClass() string {
	switch sc.Effort {
	case "S":
		return "badge-pending"
	case "M":
		return "badge-revision"
	case "L":
		return "badge-blocked"
	default:
		return "badge-pending"
	}
}

// RiskClass returns the CSS class for the risk badge.
func (sc *SliceCard) RiskClass() string {
	switch sc.Risk {
	case "High":
		return "badge-blocked"
	case "Med", "Medium":
		return "badge-revision"
	default:
		return "badge-pending"
	}
}

// ComplexityClass returns the CSS badge class for the complexity tier. Empty
// complexity returns empty (the template branch suppresses the badge).
func (sc *SliceCard) ComplexityClass() string {
	switch sc.Complexity {
	case "trivial":
		return "badge-pending"
	case "standard":
		return "badge-discuss"
	case "complex":
		return "badge-revision"
	default:
		return ""
	}
}

// ComplexityLabel returns the title-cased display label for the complexity tier.
func (sc *SliceCard) ComplexityLabel() string {
	switch sc.Complexity {
	case "trivial":
		return "Trivial"
	case "standard":
		return "Standard"
	case "complex":
		return "Complex"
	default:
		return ""
	}
}

// DecisionsNotesHTML returns the DecisionsNotes field rendered as sanitized HTML.
func (sc *SliceCard) DecisionsNotesHTML() template.HTML { return RenderMd(sc.DecisionsNotes) }

// DecisionsNotesNeedsReveal returns true when DecisionsNotes content is long
// enough to warrant a "Show full description" toggle.
func (sc *SliceCard) DecisionsNotesNeedsReveal() bool {
	return len(sc.DecisionsNotes) > revealThreshold
}

// DepsLabel returns a human-readable dependency string.
func (sc *SliceCard) DepsLabel() string {
	if sc.Deps == "" {
		return "none"
	}
	return "slices " + sc.Deps
}

// ApprovalStatusClass returns the CSS badge class for the approval status.
func (sc *SliceCard) ApprovalStatusClass() string {
	switch sc.ApprovalStatus {
	case "approved":
		return "badge-approved"
	case "rejected":
		return "badge-blocked"
	case "changes_requested":
		return "badge-revision"
	default:
		return "badge-pending"
	}
}

// ApprovalStatusLabel returns the display label for the approval status.
func (sc *SliceCard) ApprovalStatusLabel() string {
	switch sc.ApprovalStatus {
	case "approved":
		return "Approved"
	case "rejected":
		return "Rejected"
	case "changes_requested":
		return "Changes Requested"
	default:
		return "Pending"
	}
}

// ExecutionStatusLabel returns a display label for the execution status.
func (sc *SliceCard) ExecutionStatusLabel() string {
	switch sc.ExecutionStatus {
	case "not_started":
		return "Not Started"
	case "promoted":
		return "Promoted"
	case "in_progress":
		return "In Progress"
	case "done":
		return "Done"
	case "blocked":
		return "Blocked"
	case "superseded":
		return "Superseded"
	default:
		return sc.ExecutionStatus
	}
}

// ExecutionStatusClass returns the CSS badge class for the execution status.
func (sc *SliceCard) ExecutionStatusClass() string {
	switch sc.ExecutionStatus {
	case "done":
		return "badge-approved"
	case "in_progress", "promoted":
		return "badge-revision"
	case "blocked", "superseded":
		return "badge-blocked"
	default:
		return "badge-pending"
	}
}

// CriticSeverityClass returns the CSS badge class for a critic revision severity.
func (sc *SliceCard) CriticSeverityClass(severity string) string {
	switch strings.ToUpper(severity) {
	case "HIGH", "DANGER":
		return "badge-blocked"
	case "MED", "MEDIUM":
		return "badge-revision"
	default:
		return "badge-pending"
	}
}

// SliceQuestionSectionKey returns the plan_feedback section key for a
// slice-local question, following the contract: slice-<num>-question-<id>.
func (sc *SliceCard) SliceQuestionSectionKey(questionID string) string {
	return fmt.Sprintf("slice-%d-question-%s", sc.Num, questionID)
}

// SliceCardFromPlanSlice maps a planyaml.PlanSlice to a SliceCard.
// This is the canonical mapping used by enrichPageFromYAML and tests.
func SliceCardFromPlanSlice(s planyaml.PlanSlice) SliceCard {
	depsStr := ""
	for i, d := range s.Deps {
		if i > 0 {
			depsStr += ","
		}
		depsStr += fmt.Sprintf("%d", d)
	}
	filesStr := strings.Join(s.Files, ", ")

	return SliceCard{
		Num:             s.Num,
		ID:              s.ID,
		FeatureID:       s.FeatureID,
		Title:           s.Title,
		What:            s.What,
		Why:             s.Why,
		DoneWhen:        s.DoneWhen,
		Tests:           s.Tests,
		Effort:          s.Effort,
		Risk:            s.Risk,
		Complexity:      s.Complexity,
		Deps:            depsStr,
		Files:           filesStr,
		Status:          "pending",
		ApprovalStatus:  s.ApprovalStatus,
		ExecutionStatus: s.ExecutionStatus,
		Questions:       s.Questions,
		CriticRevisions: s.CriticRevisions,
		DecisionsNotes:  s.DecisionsNotes,
	}
}
