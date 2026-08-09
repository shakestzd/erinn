// Register in main.go: rootCmd.AddCommand(complianceCmd())
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// atomicWriteCounter provides a unique sequence number per atomic write call,
// used to make temp filenames unique even when called from multiple goroutines
// in the same process (which all share the same PID).
var atomicWriteCounter atomic.Int64

// writeComplianceSection replaces (or inserts) a <section class="compliance-findings"> block
// in the feature HTML file. The replacement is idempotent: any existing section is replaced
// by class match. The write is atomic via writeFileAtomicRaw.
//
// The whole read-modify-write window runs inside workitem.LockFeatureForWrite
// (in-process mutex + cross-process flock) so racing writers — including
// separate `wipnote` CLI processes — cannot lose updates.
//
// attrs is a map of data-* attribute names (without the "data-" prefix) → values.
// body is the inner HTML content for the section.
func writeComplianceSection(featurePath string, attrs map[string]string, body string) error {
	defer workitem.LockFeatureForWrite(featurePath)()

	content, err := os.ReadFile(featurePath)
	if err != nil {
		return fmt.Errorf("read feature file: %w", err)
	}

	sectionHTML := buildComplianceSectionHTML(attrs, body)
	updated := replaceOrAppendSection(string(content), sectionHTML)

	return writeFileAtomicRaw(featurePath, []byte(updated))
}

// buildComplianceSectionHTML constructs the <section class="compliance-findings"> element.
func buildComplianceSectionHTML(attrs map[string]string, body string) string {
	attrOrder := []string{"score", "cost-usd", "model", "spec-hash", "timestamp", "diff-truncated", "gate-pass", "gate-fail"}
	var sb strings.Builder
	sb.WriteString(`<section class="compliance-findings"`)
	for _, k := range attrOrder {
		if v, ok := attrs[k]; ok {
			sb.WriteString(fmt.Sprintf(` data-%s="%s"`, k, v))
		}
	}
	// Write any extra attrs not in the canonical order.
	for k, v := range attrs {
		found := false
		for _, canonical := range attrOrder {
			if k == canonical {
				found = true
				break
			}
		}
		if !found {
			sb.WriteString(fmt.Sprintf(` data-%s="%s"`, k, v))
		}
	}
	sb.WriteString(">\n")
	sb.WriteString(body)
	sb.WriteString("\n</section>")
	return sb.String()
}

// replaceOrAppendSection replaces an existing <section class="compliance-findings"> in html,
// or appends it before </body> if none exists.
func replaceOrAppendSection(html, sectionHTML string) string {
	const openTag = `<section class="compliance-findings"`
	const closeTag = `</section>`

	start := strings.Index(html, openTag)
	if start == -1 {
		// No existing section — append before </body>.
		bodyClose := strings.LastIndex(html, "</body>")
		if bodyClose == -1 {
			return html + "\n" + sectionHTML
		}
		return html[:bodyClose] + sectionHTML + "\n" + html[bodyClose:]
	}

	// Find the matching </section> after the opening tag.
	afterOpen := html[start:]
	end := strings.Index(afterOpen, closeTag)
	if end == -1 {
		// Malformed: just replace to end.
		return html[:start] + sectionHTML
	}
	end += start + len(closeTag)
	return html[:start] + sectionHTML + html[end:]
}

// writeFileAtomicRaw writes data to path atomically using temp+rename without
// acquiring the per-node mutex (caller is responsible for concurrency control
// when needed). Used by compliance write path which manages its own locking.
func writeFileAtomicRaw(path string, data []byte) error {
	seq := atomicWriteCounter.Add(1)
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), seq)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// criterionStatus represents the state of a single acceptance criterion.
type criterionStatus int

const (
	criterionUnchecked criterionStatus = iota
	criterionPassed
	criterionFailed
)

// criterion holds a parsed acceptance criterion and its status.
type criterion struct {
	Index  int             `json:"index"`
	Text   string          `json:"text"`
	Status criterionStatus `json:"status"`
}

// complianceResult holds the full compliance report for a feature.
type complianceResult struct {
	FeatureID  string      `json:"feature_id"`
	Criteria   []criterion `json:"criteria"`
	Total      int         `json:"total"`
	Passed     int         `json:"passed"`
	Failed     int         `json:"failed"`
	Unchecked  int         `json:"unchecked"`
	HasSpec    bool        `json:"has_spec"`
	HasFailure bool        `json:"has_failure"`
}

// criterionPattern matches lines like: "1. [ ] text", "2. [x] text", "- [ ] text", "- [x] text"
var criterionPattern = regexp.MustCompile(`(?i)^[\s\-\d\.]*\[([xfF\s])\]\s+(.+)$`)

func complianceCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "compliance <feature-id>",
		Short: "Score how well the implementation matches the spec's acceptance criteria",
		Long: `Read the spec section from a feature HTML file and report on each acceptance criterion.

Criteria marked with [ ] are UNCHECKED, [x] are PASSED.
Exit 0 if no failures found; exit 1 if any criteria are explicitly marked as failed.

Use 'wipnote compliance auto <id>' to run LLM-powered auto-grading.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("feature-id required")
			}
			return runCompliance(args[0], jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	// Register the auto subcommand.
	cmd.AddCommand(complianceAutoCmd())

	return cmd
}

func runCompliance(featureID string, jsonOut bool) error {
	result, err := computeCompliance(featureID)
	if err != nil {
		return err
	}

	if jsonOut {
		return printComplianceJSON(result)
	}
	return printComplianceText(result)
}

// computeCompliance reads the feature HTML and scores the spec criteria.
func computeCompliance(featureID string) (*complianceResult, error) {
	dir, err := findWipnoteDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "features", featureID+".html")
	if _, err := os.Stat(path); err != nil {
		return nil, workitem.ErrNotFound("feature", featureID)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read feature file: %w", err)
	}

	result := &complianceResult{FeatureID: featureID}

	specContent := extractSpecSection(string(content))
	if specContent == "" {
		result.HasSpec = false
		return result, nil
	}

	result.HasSpec = true
	result.Criteria = parseCriteria(specContent)
	result.Total = len(result.Criteria)

	for _, c := range result.Criteria {
		switch c.Status {
		case criterionPassed:
			result.Passed++
		case criterionFailed:
			result.Failed++
			result.HasFailure = true
		default:
			result.Unchecked++
		}
	}

	return result, nil
}

// sectionState tracks which spec section the parser is currently inside.
type sectionState int

const (
	sectionNone   sectionState = iota
	sectionLegacy              // inside ## Acceptance Criteria
	sectionNew                 // inside ## ADDED Requirements or ## MODIFIED Requirements
)

// requirementHeadingRe matches "### Requirement: <name>" headings.
var requirementHeadingRe = regexp.MustCompile(`(?i)^###\s+Requirement:\s+(.+)$`)

// parseCriteria extracts acceptance criteria from spec content.
//
// Section-state machine:
//   - "## Acceptance Criteria"          → legacy mode: parse [ ]/[x]/[F] checkbox lines
//   - "## ADDED/MODIFIED Requirements"  → new mode: parse ### Requirement: blocks
//   - any other "## " heading           → none mode (skip)
//
// Hybrid documents are supported: criteria from each section are collected
// independently with no cross-contamination.
func parseCriteria(content string) []criterion {
	var criteria []criterion
	idx := 1
	state := sectionNone

	// New-format parsing state.
	var (
		inRequirement     bool
		reqName           string
		inScenario        bool
		scenarioHasAny    bool // any scenario task lines seen
		scenarioFailed    bool // any [F] seen
		scenarioUnchecked bool // any [ ] seen
	)

	// finaliseRequirement closes the current ### Requirement block and appends a criterion.
	finaliseRequirement := func() {
		if !inRequirement || reqName == "" {
			return
		}
		status := criterionUnchecked
		if scenarioFailed {
			status = criterionFailed
		} else if scenarioHasAny && !scenarioUnchecked {
			status = criterionPassed
		}
		criteria = append(criteria, criterion{
			Index:  idx,
			Text:   reqName,
			Status: status,
		})
		idx++
		inRequirement = false
		reqName = ""
		inScenario = false
		scenarioHasAny = false
		scenarioFailed = false
		scenarioUnchecked = false
	}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		// Detect top-level (##) section changes.
		if strings.HasPrefix(trimmed, "## ") {
			// Close any open requirement before switching sections.
			if state == sectionNew {
				finaliseRequirement()
			}
			heading := trimmed[3:]
			switch {
			case strings.HasPrefix(heading, "Acceptance Criteria"):
				state = sectionLegacy
			case strings.HasPrefix(heading, "ADDED Requirements"),
				strings.HasPrefix(heading, "MODIFIED Requirements"):
				state = sectionNew
			default:
				state = sectionNone
			}
			continue
		}

		switch state {
		case sectionLegacy:
			// Only parse checkbox lines; ignore ### / #### headings entirely.
			m := criterionPattern.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			status := criterionStatusFromMatch(m[1])
			criteria = append(criteria, criterion{
				Index:  idx,
				Text:   strings.TrimSpace(m[2]),
				Status: status,
			})
			idx++

		case sectionNew:
			// Detect ### Requirement: headings.
			if strings.HasPrefix(trimmed, "### ") {
				if m := requirementHeadingRe.FindStringSubmatch(trimmed); m != nil {
					// Close previous requirement if any.
					finaliseRequirement()
					inRequirement = true
					reqName = strings.TrimSpace(m[1])
					inScenario = false
					continue
				}
			}
			// Detect #### Scenario: headings — enter scenario context.
			if strings.HasPrefix(trimmed, "#### ") && inRequirement {
				inScenario = true
				continue
			}
			// Inside a scenario, scan task lines for status.
			if inScenario && inRequirement {
				m := criterionPattern.FindStringSubmatch(trimmed)
				if m != nil {
					scenarioHasAny = true
					switch strings.ToLower(m[1]) {
					case "f":
						scenarioFailed = true
					case " ":
						scenarioUnchecked = true
					}
				}
			}
		}
	}

	// Close any open requirement at end of input.
	if state == sectionNew {
		finaliseRequirement()
	}

	return criteria
}

// criterionStatusFromMatch maps the checkbox character to a criterionStatus.
func criterionStatusFromMatch(ch string) criterionStatus {
	switch strings.ToLower(ch) {
	case "x":
		return criterionPassed
	case "f":
		return criterionFailed
	default:
		return criterionUnchecked
	}
}

// printComplianceText renders a human-readable compliance report.
func printComplianceText(r *complianceResult) error {
	if !r.HasSpec {
		fmt.Printf("No spec found for %s. Run: wipnote spec generate %s\n", r.FeatureID, r.FeatureID)
		return nil
	}

	if r.Total == 0 {
		fmt.Printf("Spec found for %s but contains no acceptance criteria.\n", r.FeatureID)
		return nil
	}

	fmt.Printf("Compliance: %s\n\n", r.FeatureID)

	for _, c := range r.Criteria {
		label, marker := criterionLabel(c.Status)
		fmt.Printf("  %s %d. %s — %s\n", marker, c.Index, c.Text, label)
	}

	fmt.Printf("\nScore: %d/%d criteria checked", r.Passed, r.Total)
	if r.Unchecked > 0 {
		fmt.Printf(" (%d unchecked)", r.Unchecked)
	}
	if r.Failed > 0 {
		fmt.Printf(" (%d failed)", r.Failed)
	}
	fmt.Println()

	if r.HasFailure {
		return fmt.Errorf("compliance check failed: %d criteria marked as failed\nRun 'wipnote compliance show <feature-id>' to review individual criteria.", r.Failed)
	}
	return nil
}

// criterionLabel returns display label and marker for a criterion status.
func criterionLabel(s criterionStatus) (string, string) {
	switch s {
	case criterionPassed:
		return "PASS", "✓"
	case criterionFailed:
		return "FAIL", "✗"
	default:
		return "UNCHECKED", "·"
	}
}

// printComplianceJSON writes the result as JSON.
func printComplianceJSON(r *complianceResult) error {
	// Reformat criteria for JSON with string status.
	type jsonCriterion struct {
		Index  int    `json:"index"`
		Text   string `json:"text"`
		Status string `json:"status"`
	}
	type jsonResult struct {
		FeatureID  string          `json:"feature_id"`
		Criteria   []jsonCriterion `json:"criteria"`
		Total      int             `json:"total"`
		Passed     int             `json:"passed"`
		Failed     int             `json:"failed"`
		Unchecked  int             `json:"unchecked"`
		HasSpec    bool            `json:"has_spec"`
		HasFailure bool            `json:"has_failure"`
	}

	out := jsonResult{
		FeatureID:  r.FeatureID,
		Total:      r.Total,
		Passed:     r.Passed,
		Failed:     r.Failed,
		Unchecked:  r.Unchecked,
		HasSpec:    r.HasSpec,
		HasFailure: r.HasFailure,
	}
	for _, c := range r.Criteria {
		var statusStr string
		switch c.Status {
		case criterionPassed:
			statusStr = "pass"
		case criterionFailed:
			statusStr = "fail"
		default:
			statusStr = "unchecked"
		}
		out.Criteria = append(out.Criteria, jsonCriterion{
			Index:  c.Index,
			Text:   c.Text,
			Status: statusStr,
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
