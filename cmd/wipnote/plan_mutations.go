package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// ---- plan set-status --------------------------------------------------------

// validPlanStatuses is the canonical list of plan statuses, sourced from
// cmd/wipnote/plan_validate.go (validStatuses map) and updatePlanStatus.
// 'active' and 'completed' are v2 lifecycle states (slice-1) that align the
// CLI vocabulary with plan/planyaml/validate.go meta.status enum.
var validPlanStatuses = []string{"todo", "draft", "in-progress", "done", "finalized", "active", "completed"}

func planSetStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-status <plan-id> <status>",
		Short: "Set the status of a plan",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanSetStatus(args[0], args[1])
		},
	}
}

func runPlanSetStatus(planID, status string) error {
	if err := validatePlanStatusArg(status); err != nil {
		return err
	}

	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	if err := updatePlanStatus(wipnoteDir, planID, status); err != nil {
		return err
	}

	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if err := commitPlanChange(yamlPath, fmt.Sprintf("plan set-status %s %s", planID, status)); err != nil {
		return fmt.Errorf("autocommit set-status: %w", err)
	}

	fmt.Printf("plan %s: status → %s\n", planID, status)
	return nil
}

// validatePlanStatusArg returns an error if status is not a valid plan status.
func validatePlanStatusArg(status string) error {
	if slices.Contains(validPlanStatuses, status) {
		return nil
	}
	return fmt.Errorf("unknown plan status %q (valid: %s)", status, strings.Join(validPlanStatuses, ", "))
}
