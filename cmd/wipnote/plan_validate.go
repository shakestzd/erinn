package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// validateProjectOpener is the function used to open the workitem project. It
// is a package-level variable so tests can inject a spy to assert that the DB
// is never opened for YAML plans.
var validateProjectOpener = workitem.Open

// planValidation holds the result of structural validation for a plan.
type planValidation struct {
	PlanID   string   `json:"plan_id"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Stats    struct {
		Slices     int `json:"slices"`
		Questions  int `json:"questions"`
		GraphNodes int `json:"graph_nodes"`
	} `json:"stats"`
}

// planValidateCmd returns the cobra command for "plan validate".
func planValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <plan-id>",
		Short: "Validate a plan's structure and content",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanValidate(args[0])
		},
	}
}

func runPlanValidate(planID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	result, err := validatePlan(wipnoteDir, planID)
	if err != nil {
		return fmt.Errorf("validate plan: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// validatePlan performs structural validation on a plan.
//
// For v2 plans (those with a .yaml file), validation runs planyaml.Validate
// directly — no SQLite/workitem.Open call is made. Legacy HTML-only plans fall
// back to the previous workitem path.
func validatePlan(wipnoteDir, planID string) (planValidation, error) {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if plan, err := planyaml.Load(yamlPath); err == nil {
		return validatePlanFromYAML(wipnoteDir, planID, plan), nil
	}

	// Legacy fallback: HTML-only plan — open workitem project (requires SQLite).
	return validatePlanFromWorkitem(wipnoteDir, planID)
}

// validatePlanFromYAML runs planyaml.Validate on the loaded plan and also
// checks the CRISPI HTML file if present, without opening any database.
func validatePlanFromYAML(wipnoteDir, planID string, plan *planyaml.PlanYAML) planValidation {
	var result planValidation
	result.PlanID = planID
	result.Valid = true

	addError := func(msg string) {
		result.Errors = append(result.Errors, msg)
		result.Valid = false
	}
	addWarning := func(msg string) {
		result.Warnings = append(result.Warnings, msg)
	}

	// Run schema validation. Completeness issues (missing design fields, slice
	// content) are reported as warnings rather than errors — the plan may be a
	// work-in-progress. Only hard structural problems (invalid meta) become errors.
	for _, e := range planyaml.Validate(plan) {
		addWarning(e)
	}

	// Research advisories: non-blocking nudges for legacy/v3 plans lacking a cited
	// research basis. v4 plans enforce this in Validate above (so this adds
	// nothing for them); for older plans it keeps web research visible before they
	// opt into the hard gate.
	for _, a := range planyaml.ValidateResearchAdvisories(plan) {
		addWarning("research: " + a)
	}

	// Block advisories: non-blocking nudges for standard/complex slices with no
	// visual blocks. Trivial slices are exempt. Never a hard error.
	for _, a := range planyaml.ValidateBlockAdvisories(plan) {
		addWarning("blocks: " + a)
	}

	// Slice stats from YAML.
	result.Stats.Slices = len(plan.Slices)
	result.Stats.GraphNodes = len(plan.Slices)

	// Warn if no slices.
	if result.Stats.Slices == 0 {
		addWarning("plan has no slices (steps)")
	}

	// Warn if no description.
	if plan.Meta.Description == "" {
		addWarning("plan has no description")
	}

	// Validate CRISPI HTML file if it exists.
	crisPIPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	if isCRISPIFile(crisPIPath) {
		htmlErrs, htmlWarnings, htmlStats := validatePlanHTML(crisPIPath)
		for _, e := range htmlErrs {
			addError(e)
		}
		for _, w := range htmlWarnings {
			addWarning(w)
		}
		if htmlStats.graphNodes > 0 {
			result.Stats.GraphNodes = htmlStats.graphNodes
		}
		if htmlStats.questions > 0 {
			result.Stats.Questions = htmlStats.questions
		}
	}

	return result
}

// validatePlanFromWorkitem reads plan data via workitem.Open (legacy path for
// HTML-only plans that predate the YAML schema).
func validatePlanFromWorkitem(wipnoteDir, planID string) (planValidation, error) {
	p, err := validateProjectOpener(wipnoteDir, agentForClaim())
	if err != nil {
		return planValidation{}, fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	node, err := p.Plans.Get(planID)
	if err != nil {
		return planValidation{}, fmt.Errorf("plan %q not found: %w", planID, err)
	}

	var result planValidation
	result.PlanID = planID
	result.Valid = true

	addError := func(msg string) {
		result.Errors = append(result.Errors, msg)
		result.Valid = false
	}

	addWarning := func(msg string) {
		result.Warnings = append(result.Warnings, msg)
	}

	// Validate status. v2 lifecycle states 'active' and 'completed' (slice-1)
	// align with plan/planyaml/validate.go meta.status enum.
	validStatuses := map[string]bool{
		"todo": true, "draft": true, "in-progress": true, "done": true, "finalized": true,
		"active": true, "completed": true,
	}
	if !validStatuses[string(node.Status)] {
		addError(fmt.Sprintf("invalid plan status %q", node.Status))
	}

	// Validate title.
	if node.Title == "" {
		addError("plan is missing a title")
	}

	// Count slices from HTML steps (YAML already tried and absent).
	result.Stats.Slices = len(node.Steps)
	result.Stats.GraphNodes = len(node.Steps)

	// Warn if no description.
	if node.Content == "" {
		addWarning("plan has no description")
	}

	// Warn if no slices.
	if result.Stats.Slices == 0 {
		addWarning("plan has no slices (steps)")
	}

	// Verify plan HTML file exists on disk.
	planPath := findPlanFile(wipnoteDir, planID)
	if planPath == "" {
		addError("plan HTML file not found on disk")
		return result, nil
	}

	// Validate the generated CRISPI HTML file if it exists and looks like one.
	crisPIPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	if isCRISPIFile(crisPIPath) {
		htmlErrs, htmlWarnings, htmlStats := validatePlanHTML(crisPIPath)
		for _, e := range htmlErrs {
			addError(e)
		}
		for _, w := range htmlWarnings {
			addWarning(w)
		}
		// HTML stats override node-level stats when the CRISPI file exists.
		if htmlStats.graphNodes > 0 {
			result.Stats.GraphNodes = htmlStats.graphNodes
		}
		if htmlStats.questions > 0 {
			result.Stats.Questions = htmlStats.questions
		}
	}

	return result, nil
}

// isCRISPIFile returns true when the HTML file contains markers indicating
// it was generated from the plan-template (CRISPI format), not a plain node.
func isCRISPIFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "btn-finalize") || strings.Contains(content, "plan-sidebar")
}

// htmlStats collects counts extracted from the HTML file.
type htmlStats struct {
	graphNodes int
	questions  int
}

// validatePlanHTML performs structural validation on the CRISPI HTML file.
// Returns errors, warnings, and extracted stats.
func validatePlanHTML(path string) (errors, warnings []string, stats htmlStats) {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("cannot read plan HTML file: %v", err)}, nil, stats
	}
	content := string(data)

	addErr := func(msg string) { errors = append(errors, msg) }
	addWarn := func(msg string) { warnings = append(warnings, msg) }

	// 1. Required HTML elements.
	requiredElements := []struct {
		marker string
		desc   string
	}{
		{`id="graph-data"`, `#graph-data element`},
		{`id="dep-graph-svg"`, `#dep-graph-svg element`},
		{`id="finalizeBtn"`, `#finalizeBtn element`},
		{`class="section-card"`, `.section-card element`},
	}
	for _, req := range requiredElements {
		if !strings.Contains(content, req.marker) {
			addErr(fmt.Sprintf("missing required HTML element: %s", req.desc))
		}
	}

	// Check .slice-card separately to warn rather than error (plans may have no slices yet).
	if !strings.Contains(content, `class="slice-card"`) {
		addWarn("no .slice-card elements found — plan has no rendered slices")
	}

	// 2. SECTIONS_JSON parseable and consistent.
	sectionsJSON, sectionsCount := extractSectionsJSON(content)
	if sectionsJSON == "" {
		addErr("SECTIONS_JSON block not found in plan HTML")
	} else {
		var sections []string
		if err := json.Unmarshal([]byte(sectionsJSON), &sections); err != nil {
			addErr(fmt.Sprintf("SECTIONS_JSON is not valid JSON: %v", err))
		} else if len(sections) != sectionsCount {
			// sectionsCount comes from PLAN_TOTAL_SECTIONS in the HTML; mismatches indicate
			// template substitution errors.
			addWarn(fmt.Sprintf("SECTIONS_JSON has %d entries but PLAN_TOTAL_SECTIONS shows %d", len(sections), sectionsCount))
		}
	}

	// 3. data-node elements must have data-name, data-deps, data-status.
	stats.graphNodes = countOccurrences(content, "data-node=")
	if stats.graphNodes > 0 {
		// Validate each data-node element has the required attributes.
		// Scope the search to the graph-data container if it exists.
		graphDataStart := strings.Index(content, `id="graph-data"`)
		if graphDataStart >= 0 {
			graphDataEnd := strings.Index(content[graphDataStart:], `</div>`)
			if graphDataEnd >= 0 {
				graphBlock := content[graphDataStart : graphDataStart+graphDataEnd]
				nodeCount := countOccurrences(graphBlock, "data-node=")
				missingName := nodeCount - countOccurrences(graphBlock, "data-name=")
				missingStatus := nodeCount - countOccurrences(graphBlock, "data-status=")
				if missingName > 0 {
					addErr(fmt.Sprintf("%d data-node element(s) are missing data-name attribute", missingName))
				}
				if missingStatus > 0 {
					addErr(fmt.Sprintf("%d data-node element(s) are missing data-status attribute", missingStatus))
				}
				// data-deps may be empty string but attribute must be present.
				if !strings.Contains(graphBlock, "data-deps=") {
					addErr("data-node elements are missing data-deps attribute")
				}
			}
		}
	}

	// 4. Approval checkboxes must have data-section and data-action attributes.
	checkboxCount := countOccurrences(content, `data-action="approve"`)
	if checkboxCount == 0 {
		addErr("no approval checkboxes found (missing data-action=\"approve\")")
	} else {
		sectionAttrCount := countOccurrences(content, `data-section=`)
		if sectionAttrCount < checkboxCount {
			addErr(fmt.Sprintf("some approval checkboxes are missing data-section attribute (%d checkboxes, %d data-section attrs)", checkboxCount, sectionAttrCount))
		}
	}

	// 5. Radio buttons must have name and value attributes.
	// Use "<input type="radio"" to avoid matching CSS/JS string references.
	radioCount := countOccurrences(content, `<input type="radio"`)
	stats.questions = countOccurrences(content, `class="question-block"`)
	if radioCount > 0 {
		nameCount := countOccurrences(content, `<input type="radio" name=`)
		if nameCount < radioCount {
			addErr(fmt.Sprintf("%d radio button(s) are missing name attribute", radioCount-nameCount))
		}
		// Each radio input should have a value= attribute. Count value= only
		// within actual <input> elements to avoid CSS/JS false positives.
		valueCount := countOccurrences(content, `<input type="radio" name=`)
		if valueCount < radioCount {
			addErr(fmt.Sprintf("%d radio button(s) may be missing value attribute", radioCount-valueCount))
		}
	}

	// 6. The dependency graph must be renderable, in either of the two formats
	//    that coexist during the migration window.
	//
	//    Plans render lazily, so the committed corpus holds both indefinitely:
	//    plans written before the static graph landed still carry an empty
	//    <svg> plus d3/dagre-d3 CDN script tags and draw themselves in the
	//    browser, while newly rendered plans carry a complete inline SVG laid
	//    out by dagre at render time and load neither library. Requiring the
	//    CDN tags unconditionally would fail every new plan; requiring the
	//    static marker unconditionally would fail all 42 existing ones. Accept
	//    both, and only complain when a plan has neither.
	if isStaticGraphPlan(content) {
		// A static graph must actually contain drawn geometry. An <svg> with
		// no <rect> and no <path> means the layout produced nothing and the
		// zone would render blank with no script left to rescue it.
		if graphNodeCount := countOccurrences(content, `class="dep-node`); graphNodeCount == 0 &&
			!strings.Contains(content, "<rect") && !strings.Contains(content, "<path") {
			addErr("dependency graph is marked static but contains no drawn nodes or edges")
		}
	} else {
		if !strings.Contains(content, "d3js.org/d3") {
			addErr("missing CDN script tag for d3.js")
		}
		// Check for the dagre-d3 CDN script src (not just the string "dagre-d3"
		// which also appears in inline JavaScript comments and variable names).
		if !strings.Contains(content, `src="https://cdn.jsdelivr.net/npm/dagre-d3`) {
			addErr("missing CDN script tag for dagre-d3")
		}
	}

	// 7. No broken HTML comments (unclosed placeholders left behind).
	brokenPlaceholders := findBrokenPlaceholders(content)
	for _, ph := range brokenPlaceholders {
		addWarn(fmt.Sprintf("unreplaced placeholder in HTML: %s", ph))
	}

	return errors, warnings, stats
}

// isStaticGraphPlan reports whether a plan's dependency graph was drawn
// server-side. The dependency zone stamps data-graph-render="static" (see
// plan/plantmpl/templates/dependency_graph.gohtml); plans predating the static
// renderer carry no such marker and are validated against the legacy
// d3/dagre-d3 contract instead.
func isStaticGraphPlan(content string) bool {
	return strings.Contains(content, `data-graph-render="static"`)
}

// extractSectionsJSON finds and returns the JSON array between
// /*PLAN_SECTIONS_JSON*/ and /*END_PLAN_SECTIONS_JSON*/ markers.
// Also parses PLAN_TOTAL_SECTIONS from the HTML for cross-validation.
// Returns empty string if not found.
func extractSectionsJSON(content string) (jsonStr string, totalSections int) {
	const start = "/*PLAN_SECTIONS_JSON*/"
	const end = "/*END_PLAN_SECTIONS_JSON*/"

	si := strings.Index(content, start)
	if si < 0 {
		return "", 0
	}
	rest := content[si+len(start):]
	ei := strings.Index(rest, end)
	if ei < 0 {
		return "", 0
	}
	jsonStr = strings.TrimSpace(rest[:ei])

	// Parse PLAN_TOTAL_SECTIONS from totalSections strong element.
	// The HTML has: <strong id="totalSections"><!--PLAN_TOTAL_SECTIONS--></strong>
	// After generation: <strong id="totalSections">4</strong>
	const tsMarker = `id="totalSections">`
	if tsi := strings.Index(content, tsMarker); tsi >= 0 {
		rest2 := content[tsi+len(tsMarker):]
		if ei2 := strings.Index(rest2, "<"); ei2 > 0 {
			val := strings.TrimSpace(rest2[:ei2])
			fmt.Sscanf(val, "%d", &totalSections) //nolint:errcheck
		}
	}

	return jsonStr, totalSections
}

// findBrokenPlaceholders returns any HTML comment placeholders that were
// not replaced during template generation. Injection-point markers
// (PLAN_GRAPH_NODES, PLAN_SLICE_CARDS, etc.) are intentionally preserved
// for idempotent add-slice / set-section calls and are NOT flagged.
func findBrokenPlaceholders(content string) []string {
	var found []string
	// Only flag placeholders that should be fully replaced (not preserved).
	for _, ph := range []string{
		"<!--PLAN_TOTAL_SECTIONS-->",
		"<!--PLAN_META-->",
	} {
		if strings.Contains(content, ph) {
			found = append(found, ph)
		}
	}
	return found
}

// countOccurrences counts non-overlapping occurrences of substr in s.
func countOccurrences(s, substr string) int {
	count := 0
	for {
		i := strings.Index(s, substr)
		if i < 0 {
			break
		}
		count++
		s = s[i+len(substr):]
	}
	return count
}
