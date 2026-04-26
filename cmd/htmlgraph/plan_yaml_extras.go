package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/planyaml"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func planListYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-yaml",
		Short: "List all YAML plans sorted by created_at descending",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runListYAML()
		},
	}
}

func runListYAML() error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	plansDir := filepath.Join(htmlgraphDir, "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No YAML plans found.")
			return nil
		}
		return fmt.Errorf("read plans dir: %w", err)
	}

	type planInfo struct {
		id        string
		status    string
		slices    int
		createdAt string
		title     string
	}

	var plans []planInfo

	// Scan for YAML plan files
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		planPath := filepath.Join(plansDir, entry.Name())

		plan, err := planyaml.Load(planPath)
		if err != nil {
			// Skip files that fail to parse
			continue
		}

		plans = append(plans, planInfo{
			id:        plan.Meta.ID,
			status:    plan.Meta.Status,
			slices:    len(plan.Slices),
			createdAt: plan.Meta.CreatedAt,
			title:     plan.Meta.Title,
		})
	}

	// Sort by created_at descending (newest first)
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].createdAt != plans[j].createdAt {
			return plans[i].createdAt > plans[j].createdAt
		}
		return plans[i].id < plans[j].id
	})

	if len(plans) == 0 {
		fmt.Println("No YAML plans found.")
		return nil
	}

	fmt.Printf("%-18s  %-11s  %-7s  %-12s  %s\n", "ID", "STATUS", "SLICES", "CREATED", "TITLE")
	fmt.Println(strings.Repeat("-", 100))
	for _, p := range plans {
		title := p.title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Printf("%-18s  %-11s  %-7d  %-12s  %s\n",
			p.id, p.status, p.slices, p.createdAt, title)
	}
	fmt.Printf("\n%d YAML plan(s)\n", len(plans))
	return nil
}

func planAddQuestionYAMLCmd() *cobra.Command {
	var description, recommended, options string
	cmd := &cobra.Command{
		Use:   "add-question-yaml <plan-id> <question-text>",
		Short: "Add a question with description and recommended option to a YAML plan",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAddQuestionYAML(args[0], args[1], description, recommended, options)
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "context paragraph (required)")
	cmd.Flags().StringVar(&recommended, "recommended", "", "recommended option key")
	cmd.Flags().StringVar(&options, "options", "", "comma-separated key:label pairs (min 2)")
	return cmd
}

func runAddQuestionYAML(planID, text, description, recommended, optionsStr string) error {
	if description == "" {
		return fmt.Errorf("--description is required")
	}
	opts := parseQuestionOptions(optionsStr)
	if len(opts) < 2 {
		return fmt.Errorf("--options must have at least 2 entries (got %d)", len(opts))
	}
	if recommended != "" {
		found := false
		for _, o := range opts {
			if o.Key == recommended {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("--recommended %q not found in options", recommended)
		}
	}
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	qid := "q-" + kebabCase(text, 40)
	plan.Questions = append(plan.Questions, planyaml.PlanQuestion{
		ID: qid, Text: text, Description: description,
		Recommended: recommended, Options: opts, Answer: nil,
	})
	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	truncated := text
	if len(truncated) > 50 {
		truncated = truncated[:50]
	}
	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): add question — %s", planID, truncated)); err != nil {
		return fmt.Errorf("autocommit add-question: %w", err)
	}

	fmt.Printf("Added question: %s (%d options)\n", qid, len(opts))
	return nil
}

func parseQuestionOptions(s string) []planyaml.QuestionOption {
	if s == "" {
		return nil
	}
	var opts []planyaml.QuestionOption
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		key, label, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		opts = append(opts, planyaml.QuestionOption{
			Key: strings.TrimSpace(key), Label: strings.TrimSpace(label),
		})
	}
	return opts
}

func kebabCase(s string, maxLen int) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > maxLen {
		s = s[:maxLen]
		s = strings.TrimRight(s, "-")
	}
	return s
}

func planSetCritiqueYAMLCmd() *cobra.Command {
	var data string
	cmd := &cobra.Command{
		Use:   "set-critique-yaml <plan-id>",
		Short: "Write AI critique data to a YAML plan (from --data or stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSetCritiqueYAML(args[0], data)
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "critique JSON (reads stdin if empty)")
	return cmd
}

func runSetCritiqueYAML(planID, dataStr string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	var jsonBytes []byte
	if dataStr != "" {
		jsonBytes = []byte(dataStr)
	} else {
		jsonBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}
	var critique planyaml.PlanCritique
	if err := json.Unmarshal(jsonBytes, &critique); err != nil {
		return fmt.Errorf("parse critique JSON: %w", err)
	}
	plan.Critique = &critique
	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): set critique — %d assumptions, %d risks",
		planID, len(critique.Assumptions), len(critique.Risks))); err != nil {
		return fmt.Errorf("autocommit set-critique: %w", err)
	}

	fmt.Printf("Critique set for %s: %d assumptions, %d risks\n",
		planID, len(critique.Assumptions), len(critique.Risks))
	return nil
}

func planValidateYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate-yaml <plan-id>",
		Short: "Validate a YAML plan's schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runValidateYAML(args[0])
		},
	}
}

func runValidateYAML(planID string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	errors := planyaml.Validate(plan)
	if len(errors) > 0 {
		for _, e := range errors {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		return fmt.Errorf("%d validation errors", len(errors))
	}
	fmt.Printf("Plan valid: %d slices, %d questions\n", len(plan.Slices), len(plan.Questions))
	return nil
}

// planReviewCmd is a deprecated command directing users to the dashboard.
func planReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:        "review <plan-id>",
		Short:      "Open a plan for review (use the dashboard instead)",
		Deprecated: "use 'htmlgraph serve' and open http://localhost:8080/#plans instead",
		Args:       cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}
}

// planSetDesignYAMLCmd sets the structured design subsections on a YAML plan.
func planSetDesignYAMLCmd() *cobra.Command {
	var problem, goals, constraints string

	cmd := &cobra.Command{
		Use:   "set-design-yaml <plan-id>",
		Short: "Set problem, goals, and constraints on a YAML plan",
		Long: `Set the structured design subsections on a YAML plan.

Example:
  htmlgraph plan set-design-yaml plan-a1b2c3d4 \
    --problem "The current system has X limitation..." \
    --goals "Goal 1,Goal 2,Goal 3" \
    --constraints "Must not break X,Must support Y"`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSetDesignYAML(args[0], problem, goals, constraints)
		},
	}
	cmd.Flags().StringVar(&problem, "problem", "", "problem statement (what's wrong and why)")
	cmd.Flags().StringVar(&goals, "goals", "", "comma-separated measurable goals")
	cmd.Flags().StringVar(&constraints, "constraints", "", "comma-separated constraints")
	return cmd
}

func runSetDesignYAML(planID, problem, goals, constraints string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	if problem != "" {
		plan.Design.Problem = problem
	}
	if goals != "" {
		plan.Design.Goals = splitTrimmed(goals)
	}
	if constraints != "" {
		plan.Design.Constraints = splitTrimmed(constraints)
	}
	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): update design", planID)); err != nil {
		return fmt.Errorf("autocommit set-design: %w", err)
	}

	fmt.Printf("Design updated for %s: problem=%v goals=%d constraints=%d\n",
		planID, problem != "", len(plan.Design.Goals), len(plan.Design.Constraints))
	return nil
}

func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// planRewriteYAMLCmd replaces an existing YAML plan file with validated new content.
func planRewriteYAMLCmd() *cobra.Command {
	var filePath string

	cmd := &cobra.Command{
		Use:   "rewrite-yaml <plan-id>",
		Short: "Replace a YAML plan file with validated new content",
		Long: `Replace an existing YAML plan file with new content from a file or stdin.

The new content is validated before writing. The meta.id in the new content
must match the existing plan ID to prevent accidental overwrites.

Example:
  htmlgraph plan rewrite-yaml plan-abc12345 --file /tmp/updated-plan.yaml
  cat /tmp/updated-plan.yaml | htmlgraph plan rewrite-yaml plan-abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRewriteYAML(args[0], filePath)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "path to YAML file (reads stdin if empty)")
	return cmd
}

func runRewriteYAML(planID, filePath string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")

	// Confirm the plan exists.
	if _, err := os.Stat(planPath); err != nil {
		return fmt.Errorf("plan %q not found at %s", planID, planPath)
	}

	// Read the new YAML content.
	var yamlBytes []byte
	if filePath != "" {
		yamlBytes, err = os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read file %q: %w", filePath, err)
		}
	} else {
		yamlBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	// Parse with planyaml.Load equivalent — unmarshal into PlanYAML.
	newPlan, err := planyaml.LoadBytes(yamlBytes)
	if err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}

	// Validate.
	errs := planyaml.Validate(newPlan)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		return fmt.Errorf("%d validation error(s)", len(errs))
	}

	// Enforce meta.id matches the canonical plan ID (derived from HTML filename).
	if newPlan.Meta.ID != planID {
		fmt.Fprintf(os.Stderr, "warning: meta.id %q overwritten to match plan ID %q\n", newPlan.Meta.ID, planID)
		newPlan.Meta.ID = planID
	}

	// Apply accepted amendments from plan_feedback before saving.
	amendmentsApplied, err := applyAcceptedAmendments(htmlgraphDir, planID, newPlan)
	if err != nil {
		return fmt.Errorf("apply amendments: %w", err)
	}

	// Write validated content.
	if err := planyaml.Save(planPath, newPlan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): rewrite — %d slices, %d questions",
		planID, len(newPlan.Slices), len(newPlan.Questions))); err != nil {
		return fmt.Errorf("autocommit rewrite: %w", err)
	}

	if amendmentsApplied > 0 {
		fmt.Printf("Amendments applied: %d\n", amendmentsApplied)
	}
	fmt.Printf("Plan %s rewritten: %d slices, %d questions\n", planID, len(newPlan.Slices), len(newPlan.Questions))
	return nil
}

// amendmentValue is the JSON payload stored in plan_feedback.value for amendments.
type amendmentValue struct {
	SliceNum  int    `json:"slice_num"`
	Operation string `json:"operation"`
	Field     string `json:"field"`
	Content   string `json:"content"`
}

// applyAcceptedAmendments queries accepted amendments for planID, applies them
// to the in-memory plan, marks them applied in the DB, and returns the count.
func applyAcceptedAmendments(htmlgraphDir, planID string, plan *planyaml.PlanYAML) (int, error) {
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return 0, fmt.Errorf("resolve db path: %w", err)
	}
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		return 0, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT question_id, value FROM plan_feedback WHERE plan_id = ? AND section = 'amendment' AND action = 'accepted'`,
		planID,
	)
	if err != nil {
		return 0, fmt.Errorf("query amendments: %w", err)
	}
	defer rows.Close()

	type pendingAmendment struct {
		questionID string
		amendment  amendmentValue
	}
	var pending []pendingAmendment
	for rows.Next() {
		var qid, value string
		if err := rows.Scan(&qid, &value); err != nil {
			return 0, fmt.Errorf("scan amendment row: %w", err)
		}
		var a amendmentValue
		if err := json.Unmarshal([]byte(value), &a); err != nil {
			return 0, fmt.Errorf("parse amendment JSON (question_id=%s): %w", qid, err)
		}
		pending = append(pending, pendingAmendment{questionID: qid, amendment: a})
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate amendment rows: %w", err)
	}
	rows.Close()

	if len(pending) == 0 {
		return 0, nil
	}

	// Apply each amendment to the in-memory plan.
	applied := 0
	for _, p := range pending {
		a := p.amendment
		// Find target slice by slice_num (1-indexed).
		sliceIdx := -1
		for i, s := range plan.Slices {
			if s.Num == a.SliceNum {
				sliceIdx = i
				break
			}
		}
		if sliceIdx < 0 {
			// Slice not found — skip but still mark applied so we don't retry forever.
			fmt.Fprintf(os.Stderr, "amendment %s: slice_num %d not found, skipping\n", p.questionID, a.SliceNum)
		} else {
			switch a.Operation {
			case "add":
				switch a.Field {
				case "done_when":
					plan.Slices[sliceIdx].DoneWhen = append(plan.Slices[sliceIdx].DoneWhen, a.Content)
				case "files":
					plan.Slices[sliceIdx].Files = append(plan.Slices[sliceIdx].Files, a.Content)
				}
			case "remove":
				switch a.Field {
				case "done_when":
					plan.Slices[sliceIdx].DoneWhen = removeString(plan.Slices[sliceIdx].DoneWhen, a.Content)
				case "files":
					plan.Slices[sliceIdx].Files = removeString(plan.Slices[sliceIdx].Files, a.Content)
				}
			case "set":
				switch a.Field {
				case "title":
					plan.Slices[sliceIdx].Title = a.Content
				case "what":
					plan.Slices[sliceIdx].What = a.Content
				case "why":
					plan.Slices[sliceIdx].Why = a.Content
				case "effort":
					plan.Slices[sliceIdx].Effort = a.Content
				case "risk":
					plan.Slices[sliceIdx].Risk = a.Content
				}
			}
		}

		// Mark as applied regardless of whether the slice was found.
		_, err = db.Exec(
			`UPDATE plan_feedback SET action = 'applied' WHERE plan_id = ? AND question_id = ?`,
			planID, p.questionID,
		)
		if err != nil {
			return applied, fmt.Errorf("mark amendment applied (question_id=%s): %w", p.questionID, err)
		}
		applied++
	}

	return applied, nil
}

// removeString returns a copy of ss with all occurrences of s removed.
func removeString(ss []string, s string) []string {
	var result []string
	for _, v := range ss {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}

// planReadFeedbackYAMLCmd queries plan_feedback for a YAML plan and outputs JSON.
func planReadFeedbackYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read-feedback-yaml <plan-id>",
		Short: "Read human feedback for a YAML plan from SQLite",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runReadFeedbackYAML(args[0])
		},
	}
}

func runReadFeedbackYAML(planID string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	// Read YAML status.
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	// Query SQLite.
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT section, action, value, question_id FROM plan_feedback WHERE plan_id = ?", planID)
	if err != nil {
		return fmt.Errorf("query feedback: %w", err)
	}
	defer rows.Close()

	type feedbackResult struct {
		PlanID          string            `json:"plan_id"`
		Status          string            `json:"status"`
		DesignApproved  bool              `json:"design_approved"`
		DesignComment   string            `json:"design_comment,omitempty"`
		SliceApprovals  map[string]bool   `json:"slice_approvals"`
		QuestionAnswers map[string]string `json:"question_answers"`
		Comments        map[string]string `json:"comments"`
	}
	result := feedbackResult{
		PlanID:          planID,
		Status:          plan.Meta.Status,
		SliceApprovals:  make(map[string]bool),
		QuestionAnswers: make(map[string]string),
		Comments:        make(map[string]string),
	}
	for rows.Next() {
		var section, action, value, qid string
		if err := rows.Scan(&section, &action, &value, &qid); err != nil {
			return fmt.Errorf("scan feedback row: %w", err)
		}
		switch action {
		case "approve":
			if section == "design" {
				result.DesignApproved = strings.EqualFold(value, "true")
			} else {
				result.SliceApprovals[section] = strings.EqualFold(value, "true")
			}
		case "comment":
			if section == "design" {
				result.DesignComment = value
			} else {
				result.Comments[section] = value
			}
		case "answer":
			result.QuestionAnswers[qid] = value
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate feedback rows: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
