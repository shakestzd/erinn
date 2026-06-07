package main

import (
	"fmt"
	"path/filepath"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// planReopenCmd creates a cobra command for plan reopen.
func planReopenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <plan-id>",
		Short: "Unlock a finalized plan so slices can be edited",
		Long: `Reopen a finalized plan by setting its status back to 'todo'.
Promoted features are NOT deleted — they have their own lifecycle.
Adding or editing slices after reopen will create NEW features on the next finalize.

Example:
  wipnote plan reopen plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			if err := executePlanReopen(wipnoteDir, args[0]); err != nil {
				return err
			}
			fmt.Printf("Plan %s reopened (status: todo).\n", args[0])
			fmt.Println("Warning: promoted features are not deleted; editing slices will create NEW features on next finalize.")
			return nil
		},
	}
}

// executePlanReopen unlocks a finalized plan by setting its YAML status back to "todo".
// Promoted features are not deleted.
func executePlanReopen(wipnoteDir, planID string) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan YAML for %s: %w", planID, err)
	}

	if plan.Meta.Status != "finalized" {
		return fmt.Errorf("plan %s is not finalized (status: %q) — nothing to reopen", planID, plan.Meta.Status)
	}

	plan.Meta.Status = "todo"
	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan YAML: %w", err)
	}

	// Re-render HTML to reflect new status.
	_ = renderPlanToFile(wipnoteDir, planID)

	commitMsg := fmt.Sprintf("plan(%s): reopen", planID)
	if err := commitPlanChange(planPath, commitMsg); err != nil {
		return fmt.Errorf("autocommit reopen: %w", err)
	}

	return nil
}
