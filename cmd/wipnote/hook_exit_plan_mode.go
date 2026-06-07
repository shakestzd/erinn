package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/internal/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
)

// handleExitPlanMode handles the PostToolUse ExitPlanMode event. It finds the
// most recently modified markdown file in .wipnote/plans/, parses it into a
// best-effort CRISPI YAML plan, saves it, and suggests a review command.
//
// This handler lives in cmd/wipnote (not internal/hooks) because the hooks
// package must not import workitem (spike creation policy). The markdown-to-YAML
// conversion uses workitem.GenerateID and planyaml.NewPlan/Save.
func handleExitPlanMode(event *hooks.CloudEvent, database *sql.DB, projectDir string) (*hooks.HookResult, error) {
	plansDir := filepath.Join(projectDir, ".wipnote", "plans")

	mdPath, err := mostRecentMarkdownFile(plansDir)
	if err != nil {
		hooks.LogError("exit-plan-mode", event.SessionID, fmt.Sprintf("no markdown plan found: %v", err))
		return &hooks.HookResult{Continue: true}, nil
	}

	data, err := os.ReadFile(mdPath)
	if err != nil {
		hooks.LogError("exit-plan-mode", event.SessionID, fmt.Sprintf("cannot read %s: %v", mdPath, err))
		return &hooks.HookResult{Continue: true}, nil
	}

	title, description := extractPlanTitleFromMarkdown(string(data))
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(mdPath), ".md")
	}

	planID := workitem.GenerateID("plan", title)
	plan := planyaml.NewPlan(planID, title, description)

	parsed := parseMarkdown(string(data))
	plan.Slices = parsed.slices

	// Populate design section from structural headings.
	if parsed.problem != "" {
		plan.Design.Problem = parsed.problem
	}
	if len(parsed.doneWhen) > 0 && len(plan.Slices) > 0 {
		plan.Slices[0].DoneWhen = parsed.doneWhen
	}

	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		hooks.LogError("exit-plan-mode", event.SessionID, fmt.Sprintf("mkdir plans: %v", err))
		return &hooks.HookResult{Continue: true}, nil
	}

	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		hooks.LogError("exit-plan-mode", event.SessionID, fmt.Sprintf("save YAML: %v", err))
		return &hooks.HookResult{Continue: true}, nil
	}

	if err := commitPlanChange(yamlPath, fmt.Sprintf("plan(%s): auto-convert from plan mode — %s", planID, title)); err != nil {
		hooks.LogError("exit-plan-mode", event.SessionID, fmt.Sprintf("autocommit: %v", err))
	}

	suggestion := fmt.Sprintf(
		"Plan mode output converted to CRISPI YAML: %s\nRun: wipnote plan review %s",
		yamlPath, planID)

	return &hooks.HookResult{
		Continue:          true,
		AdditionalContext: suggestion,
	}, nil
}

// mostRecentMarkdownFile returns the path to the most recently modified .md
// file in the given directory. Returns an error if no markdown files exist.
func mostRecentMarkdownFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read plans dir: %w", err)
	}

	var bestPath string
	var bestTime time.Time

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestPath = filepath.Join(dir, e.Name())
		}
	}

	if bestPath == "" {
		return "", fmt.Errorf("no .md files in %s", dir)
	}
	return bestPath, nil
}

// extractPlanTitleFromMarkdown extracts the first H1 heading as the plan
// title and the first non-heading paragraph after it as the description.
func extractPlanTitleFromMarkdown(content string) (title, description string) {
	lines := strings.Split(content, "\n")
	titleFound := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !titleFound && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			titleFound = true
			continue
		}

		if titleFound && description == "" && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			description = trimmed
			break
		}
	}
	return title, description
}

// mdFilePathPattern matches file paths mentioned in markdown text. Looks for
// paths with slashes or common source file extensions.
var mdFilePathPattern = regexp.MustCompile(`(?:^|\s)([\w./\-]+\.(?:go|py|ts|tsx|js|jsx|yaml|yml|json|html|css|sql|sh|toml|mod))`)

// structuralHeadings is the set of markdown headings (lowercased, trimmed)
// that represent plan metadata sections rather than delivery slices. Content
// under these headings is routed to the design section, not to slices.
var structuralHeadings = map[string]struct{}{
	"context":              {},
	"background":           {},
	"overview":             {},
	"summary":              {},
	"verification":         {},
	"testing":              {},
	"test plan":            {},
	"tests":                {},
	"performance":          {},
	"performance budget":   {},
	"files changed":        {},
	"file changes":         {},
	"key files":            {},
	"dependencies":         {},
	"prerequisites":        {},
	"requirements":         {},
	"risk":                 {},
	"risks":                {},
	"risk assessment":      {},
	"notes":                {},
	"open questions":       {},
	"key design decisions": {},
}

// isStructuralHeading reports whether a heading title represents a plan
// metadata section rather than a delivery slice.
func isStructuralHeading(title string) bool {
	_, ok := structuralHeadings[strings.ToLower(strings.TrimSpace(title))]
	return ok
}

// parsedPlanData holds intermediate results from markdown parsing before
// the caller assembles the final YAML plan.
type parsedPlanData struct {
	slices      []planyaml.PlanSlice
	problem     string   // from Context/Background/Overview heading
	constraints []string // from Verification/Testing heading bullets
	doneWhen    []string // from Verification/Testing heading bullets
}

// parseMarkdownToSlices parses markdown headings and their content into
// PlanSlice structs. H2/H3 headings that match structuralHeadings are skipped
// as delivery slices; their content is routed to design metadata instead.
// Bullet lists under delivery headings populate the "what" field. File paths
// mentioned in text populate the "files" field.
// Defaults: effort=M, risk=Med, approved=false.
func parseMarkdownToSlices(content string) []planyaml.PlanSlice {
	result := parseMarkdown(content)
	return result.slices
}

// parseMarkdown performs full markdown parsing returning both slices and
// structural metadata extracted from non-delivery sections.
func parseMarkdown(content string) parsedPlanData {
	lines := strings.Split(content, "\n")
	var slices []planyaml.PlanSlice

	var currentTitle string
	var currentWhat []string
	var currentFiles []string
	filesSeen := map[string]bool{}
	sliceNum := 0
	isStructural := false

	var problemLines []string
	var verificationBullets []string
	var inProblemSection bool
	var inVerificationSection bool

	flushSlice := func() {
		if currentTitle == "" {
			return
		}
		if isStructural {
			// Route structural section content to metadata, not slices.
			currentTitle = ""
			currentWhat = nil
			currentFiles = nil
			filesSeen = map[string]bool{}
			isStructural = false
			inProblemSection = false
			inVerificationSection = false
			return
		}
		sliceNum++
		what := strings.Join(currentWhat, "\n")
		if what == "" {
			what = currentTitle
		}
		slices = append(slices, planyaml.PlanSlice{
			ID:       workitem.GenerateID("feat", currentTitle),
			Num:      sliceNum,
			Title:    currentTitle,
			What:     strings.TrimSpace(what),
			Files:    currentFiles,
			Effort:   "M",
			Risk:     "Med",
			Approved: false,
		})
		currentTitle = ""
		currentWhat = nil
		currentFiles = nil
		filesSeen = map[string]bool{}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect H2/H3 headings as slice boundaries.
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			flushSlice()
			currentTitle = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
			isStructural = isStructuralHeading(currentTitle)

			lower := strings.ToLower(currentTitle)
			inProblemSection = lower == "context" || lower == "background" || lower == "overview"
			inVerificationSection = lower == "verification" || lower == "testing" || lower == "test plan" || lower == "tests"
			continue
		}

		// Skip H1 (plan title).
		if strings.HasPrefix(trimmed, "# ") {
			continue
		}

		if inProblemSection {
			if trimmed != "" {
				problemLines = append(problemLines, trimmed)
			}
			continue
		}

		if inVerificationSection {
			if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
				verificationBullets = append(verificationBullets, strings.TrimSpace(trimmed[2:]))
			}
			continue
		}

		if isStructural {
			// Skip content of other structural sections entirely.
			continue
		}

		// Accumulate bullet items as "what" content.
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			item := strings.TrimSpace(trimmed[2:])
			currentWhat = append(currentWhat, item)
		} else if trimmed != "" && currentTitle != "" {
			currentWhat = append(currentWhat, trimmed)
		}

		// Extract file paths.
		for _, match := range mdFilePathPattern.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 && !filesSeen[match[1]] {
				filesSeen[match[1]] = true
				currentFiles = append(currentFiles, match[1])
			}
		}
	}

	flushSlice()

	return parsedPlanData{
		slices:   slices,
		problem:  strings.Join(problemLines, " "),
		doneWhen: verificationBullets,
	}
}
