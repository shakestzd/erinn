package main

import (
	"fmt"
	"os"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// featureReopenCmd creates a cobra command for feature reopen.
func featureReopenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <feature-id>",
		Short: "Reopen a completed feature, setting it back to in-progress",
		Long: `Reopen a completed feature by setting its status back to 'in-progress'.
This is a non-destructive operation — all history is preserved.

Errors if the feature is not currently done.

Example:
  wipnote feature reopen feat-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := executeFeatureReopen(args[0]); err != nil {
				return err
			}
			fmt.Printf("Feature %s reopened (status: in-progress).\n", args[0])
			return nil
		},
	}
}

// executeFeatureReopen sets a done feature back to in-progress.
func executeFeatureReopen(featureID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	featureID, err = resolveID(wipnoteDir, featureID)
	if err != nil {
		return err
	}

	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	node, err := p.Features.Get(featureID)
	if err != nil {
		return fmt.Errorf("get feature %s: %w", featureID, err)
	}

	if node.Status != models.StatusDone {
		return fmt.Errorf("feature %s is not done (status: %q) — nothing to reopen", featureID, node.Status)
	}

	if _, err := p.Features.Start(featureID); err != nil {
		return fmt.Errorf("reopen feature %s: %w", featureID, err)
	}

	// Auto-commit the reopened HTML so the state transition is durable
	// (feat-712f9194 / roborev #1678). Non-fatal — failures log to stderr.
	if shouldAutocommitWorkitemArtifact("feature") {
		if commitErr := commitWipnoteArtifact(wipnoteDir, "feature", featureID, "reopen"); commitErr != nil {
			fmt.Fprintf(os.Stderr, "autocommit warning: %v\n", commitErr)
		}
	}

	return nil
}
