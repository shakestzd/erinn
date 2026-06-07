package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// --- update and move commands ------------------------------------------------

type wiUpdateOpts struct {
	trackID  string
	title    string
	priority string
}

func wiUpdateCmd(typeName string) *cobra.Command {
	var opts wiUpdateOpts
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update " + typeName + " metadata (title, priority, track)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiUpdate(typeName, args[0], &opts)
		},
	}
	cmd.Flags().StringVar(&opts.title, "title", "", "new title")
	cmd.Flags().StringVar(&opts.priority, "priority", "", "new priority (low|medium|high|critical)")
	if typeName != "track" {
		cmd.Flags().StringVar(&opts.trackID, "track", "", "reassign to track")
	}
	return cmd
}

func runWiUpdate(typeName, id string, o *wiUpdateOpts) error {
	if o.title == "" && o.priority == "" && o.trackID == "" {
		return fmt.Errorf("at least one of --title, --priority, or --track is required")
	}
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	id, err = resolveID(dir, id)
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	edit := col.Edit(id)
	if o.title != "" {
		edit = edit.SetTitle(o.title)
	}
	if o.priority != "" {
		edit = edit.SetPriority(o.priority)
	}
	if o.trackID != "" {
		// Validate target track exists.
		if _, err := p.Tracks.Get(o.trackID); err != nil {
			return fmt.Errorf("track %s not found\nRun 'wipnote track list' to see available tracks", o.trackID)
		}
		edit = edit.SetTrack(o.trackID)
	}
	if err := edit.Save(); err != nil {
		return fmt.Errorf("update %s: %w", id, err)
	}
	if typeName == "plan" && o.priority != "" {
		if err := updatePlanYAMLPriority(dir, id, o.priority); err != nil {
			return err
		}
	}

	// If track changed, update edges.
	if o.trackID != "" {
		if err := moveTrackEdges(p, id, typeName, o.trackID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: edge update failed: %v\n", err)
		}
	}

	fmt.Printf("Updated: %s\n", id)
	return nil
}

func updatePlanYAMLPriority(wipnoteDir, planID, priority string) error {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	unlock := planyaml.LockPlanForWrite(yamlPath)
	defer unlock()
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return fmt.Errorf("load plan YAML %s: %w", planID, err)
	}
	plan.Meta.Priority = priority
	if err := planyaml.SaveLocked(yamlPath, plan); err != nil {
		return fmt.Errorf("save plan YAML %s: %w", planID, err)
	}
	if err := renderPlanToFileQuiet(wipnoteDir, planID); err != nil {
		return fmt.Errorf("render plan %s: %w", planID, err)
	}
	return nil
}

func wiMoveCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "move <id> <target-track-id>",
		Short: "Move " + typeName + " to a different track",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiMove(typeName, args[0], args[1])
		},
	}
}

func runWiMove(typeName, id, targetTrackID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	id, err = resolveID(dir, id)
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	node, err := col.Get(id)
	if err != nil {
		return fmt.Errorf("%s %s not found: %w\nRun 'wipnote %s list' to see valid IDs.", typeName, id, err, typeName)
	}

	// Validate target track exists.
	track, err := p.Tracks.Get(targetTrackID)
	if err != nil {
		return fmt.Errorf("track %s not found\nRun 'wipnote track list' to see available tracks", targetTrackID)
	}

	if node.TrackID == targetTrackID {
		fmt.Printf("Already in track %s\n", targetTrackID)
		return nil
	}

	// Update track ID on the item.
	if err := col.Edit(id).SetTrack(targetTrackID).Save(); err != nil {
		return fmt.Errorf("update track: %w", err)
	}

	// Update edges.
	if err := moveTrackEdges(p, id, typeName, targetTrackID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: edge update failed: %v\n", err)
	}

	fmt.Printf("Moved: %s → %s (%s)\n", id, targetTrackID, track.Title)
	return nil
}

// moveTrackEdges removes old track edges and creates new ones.
func moveTrackEdges(p *workitem.Project, itemID, typeName, newTrackID string) error {
	col := collectionFor(p, typeName)
	node, err := col.Get(itemID)
	if err != nil {
		return err
	}

	// Remove old part_of edges to any track.
	for _, e := range node.Edges[string(models.RelPartOf)] {
		if strings.HasPrefix(e.TargetID, "trk-") {
			col.RemoveEdge(itemID, e.TargetID, models.RelPartOf)
			p.Tracks.RemoveEdge(e.TargetID, itemID, models.RelContains)
		}
	}

	// Create new edges.
	return autoTrackEdges(p, itemID, typeName, newTrackID, node.Title)
}
