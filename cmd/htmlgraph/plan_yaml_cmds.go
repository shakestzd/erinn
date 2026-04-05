package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/planyaml"
	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

// planCreateYAMLCmd creates a YAML plan file with empty design, slices,
// questions, and nil critique. This is the YAML counterpart of "plan create".
func planCreateYAMLCmd() *cobra.Command {
	var description string
	var trackID string

	cmd := &cobra.Command{
		Use:   "create-yaml <title>",
		Short: "Create a YAML plan file",
		Long: `Create a plan file in YAML format with empty design, slices,
questions, and no critique section.

Unlike the HTML 'plan create', this produces a machine-readable YAML file
suitable for programmatic editing by agents and scripts.

Example:
  htmlgraph plan create-yaml "Auth Middleware Rewrite" --description "Rewrite for compliance" --track trk-abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanCreateYAML(args[0], description, trackID)
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "plan description")
	cmd.Flags().StringVar(&trackID, "track", "", "parent track ID (e.g. trk-abc12345)")
	return cmd
}

// planSetCritiqueCmd writes structured critique data to a YAML plan file.
// Accepts JSON either from the --data flag or from stdin.
func planSetCritiqueCmd() *cobra.Command {
	var data string

	cmd := &cobra.Command{
		Use:   "set-critique <plan-id>",
		Short: "Write critique data to a YAML plan file",
		Long: `Write structured critique data (assumptions, critics, risks, synthesis)
to a YAML plan file. Accepts JSON from the --data flag or from stdin.

Example (flag):
  htmlgraph plan set-critique plan-abc12345 --data '{"reviewed_at":"2026-04-05",...}'

Example (stdin):
  cat critique.json | htmlgraph plan set-critique plan-abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			htmlgraphDir, err := findHtmlgraphDir()
			if err != nil {
				return err
			}
			return runPlanSetCritique(htmlgraphDir, args[0], data)
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "critique JSON (reads from stdin if omitted)")
	return cmd
}

// runPlanSetCritique loads the YAML plan, unmarshals the critique JSON (from
// jsonData or stdin when jsonData is empty), sets plan.Critique, and saves.
func runPlanSetCritique(htmlgraphDir, planID, jsonData string) error {
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")

	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", planID, err)
	}

	if jsonData == "" {
		raw, readErr := io.ReadAll(os.Stdin)
		if readErr != nil {
			return fmt.Errorf("read stdin: %w", readErr)
		}
		jsonData = string(raw)
	}

	var critique planyaml.PlanCritique
	if err := json.Unmarshal([]byte(jsonData), &critique); err != nil {
		return fmt.Errorf("unmarshal critique JSON: %w", err)
	}

	plan.Critique = &critique

	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan %q: %w", planID, err)
	}

	fmt.Printf("Critique set for %s: %d assumptions, %d risks\n",
		planID, len(critique.Assumptions), len(critique.Risks))
	return nil
}

// planValidateYAMLCmd validates a YAML plan file against the schema.
func planValidateYAMLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate-yaml <plan-id>",
		Short: "Validate a YAML plan file against the schema",
		Long: `Load a YAML plan file and validate it against the schema rules.

Checks meta fields, design completeness, slice integrity (effort, risk, deps),
and question structure. Prints all errors found, or a summary if valid.

Example:
  htmlgraph plan validate-yaml plan-abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			htmlgraphDir, err := findHtmlgraphDir()
			if err != nil {
				return err
			}
			return runPlanValidateYAML(htmlgraphDir, args[0])
		},
	}
}

// runPlanValidateYAML loads the YAML plan, runs Validate(), and reports results.
func runPlanValidateYAML(htmlgraphDir, planID string) error {
	planPath := filepath.Join(htmlgraphDir, "plans", planID+".yaml")

	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", planID, err)
	}

	errs := planyaml.Validate(plan)
	if len(errs) > 0 {
		fmt.Printf("Plan %s has %d validation error(s):\n", planID, len(errs))
		fmt.Println(strings.Join(errs, "\n"))
		return fmt.Errorf("validation failed")
	}

	fmt.Printf("Plan valid: %d slice(s), %d question(s)\n", len(plan.Slices), len(plan.Questions))
	return nil
}

// runPlanCreateYAML generates a YAML plan file and prints its path.
func runPlanCreateYAML(title, description, trackID string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	planID := workitem.GenerateID("plan", title)
	plan := planyaml.NewPlan(planID, title, description)

	if trackID != "" {
		plan.Meta.TrackID = trackID
	}

	plansDir := filepath.Join(htmlgraphDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}

	outPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(outPath, plan); err != nil {
		return fmt.Errorf("save plan YAML: %w", err)
	}

	fmt.Println(outPath)
	return nil
}
