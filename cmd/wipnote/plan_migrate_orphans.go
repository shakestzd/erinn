package main

import (
	"fmt"
	"strings"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// planMigrateOrphansCmd creates a cobra command for plan migrate-orphans.
func planMigrateOrphansCmd() *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "migrate-orphans",
		Short: "Find features with no plan and mark them standalone",
		Long: `Walk all features and find those whose part_of edges contain no plan-* target.
These are "orphan" features that predate the plan hierarchy enforcement.

Dry-run by default: prints count and IDs.
Use --apply to mark each orphan with standalone_reason=pre-enforcement.

Example:
  wipnote plan migrate-orphans
  wipnote plan migrate-orphans --apply`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			p, err := workitem.Open(wipnoteDir, agentForClaim())
			if err != nil {
				return fmt.Errorf("open project: %w", err)
			}
			defer p.Close()

			return executeMigrateOrphans(p, apply)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "mark orphan features standalone (default: dry-run)")
	return cmd
}

// executeMigrateOrphans finds orphan features and optionally marks them standalone.
// An orphan feature has no part_of edge pointing to a plan-* ID.
func executeMigrateOrphans(p *workitem.Project, apply bool) error {
	features, err := p.Features.List()
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}

	var orphans []*models.Node
	for _, feat := range features {
		if isOrphanFeature(feat) {
			orphans = append(orphans, feat)
		}
	}

	if len(orphans) == 0 {
		fmt.Println("No orphan features found.")
		return nil
	}

	if !apply {
		fmt.Printf("Orphan features (no plan linkage): %d\n", len(orphans))
		for _, f := range orphans {
			fmt.Printf("  %s  %s\n", f.ID, truncate(f.Title, 50))
		}
		fmt.Println("\nRe-run with --apply to mark these features as standalone.")
		return nil
	}

	// Apply: mark each orphan as standalone.
	applied := 0
	for _, feat := range orphans {
		edit := p.Features.Edit(feat.ID)
		edit = edit.SetProperty("standalone_reason", "pre-enforcement")
		if err := edit.Save(); err != nil {
			fmt.Printf("  Warning: failed to mark %s standalone: %v\n", feat.ID, err)
			continue
		}
		applied++
		fmt.Printf("  marked standalone: %s  %s\n", feat.ID, truncate(feat.Title, 50))
	}

	fmt.Printf("\nMarked %d of %d orphan features as standalone.\n", applied, len(orphans))
	return nil
}

// isOrphanFeature returns true when the feature has no edge linking it to a
// plan. Both part_of → plan-* and planned_in → plan-* count as plan linkage —
// plan finalize and feature create --plan write planned_in, while older
// scaffolding may have used part_of. Features with an explicit
// standalone_reason are also excluded (they've already been handled).
func isOrphanFeature(feat *models.Node) bool {
	if v, ok := feat.Properties["standalone_reason"]; ok && v != "" {
		return false
	}

	for _, rel := range []models.RelationshipType{models.RelPartOf, models.RelPlannedIn} {
		for _, edge := range feat.Edges[string(rel)] {
			if strings.HasPrefix(edge.TargetID, "plan-") {
				return false
			}
		}
	}
	return true
}
