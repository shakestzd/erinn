package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/hooks"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

// workitemCmd builds a standard CRUD command group for any work item type.
// Usage: workitemCmd("feature", "features"), workitemCmd("bug", "bugs"), etc.
func workitemCmd(typeName, dirName string) *cobra.Command {
	cmd := &cobra.Command{Use: typeName, Short: "Manage " + dirName}
	cmd.AddCommand(wiCreateCmd(typeName, dirName))
	cmd.AddCommand(wiListCmd(typeName, dirName))
	cmd.AddCommand(wiShowCmd(typeName))
	cmd.AddCommand(wiStartCmd(typeName))
	cmd.AddCommand(wiCompleteCmd(typeName))
	cmd.AddCommand(wiDeleteCmd(typeName))
	cmd.AddCommand(wiAddStepCmd(typeName))
	cmd.AddCommand(wiRemoveStepCmd(typeName))
	cmd.AddCommand(wiUpdateStepCmd(typeName))
	cmd.AddCommand(wiCompleteStepCmd(typeName))
	cmd.AddCommand(wiEditDescriptionCmd(typeName))
	return cmd
}

type wiCreateOpts struct {
	trackID     string
	priority    string
	description string
	files       string
	start       bool
	noLink      bool
}

func wiCreateCmd(typeName, dirName string) *cobra.Command {
	var opts wiCreateOpts
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a new " + typeName,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiCreate(typeName, args[0], &opts)
		},
	}
	cmd.Flags().StringVar(&opts.trackID, "track", "", "track ID to link to")
	cmd.Flags().StringVar(&opts.priority, "priority", "medium", "priority (low|medium|high|critical)")
	cmd.Flags().StringVar(&opts.description, "description", "", "description text")
	cmd.Flags().BoolVar(&opts.start, "start", false, "immediately mark as in-progress")
	cmd.Flags().BoolVar(&opts.noLink, "no-link", false, "skip auto-linking (e.g. bug to active feature)")
	if typeName == "bug" {
		cmd.Flags().StringVar(&opts.files, "files", "", "comma-separated affected file paths")
	}
	return cmd
}

func runWiCreate(typeName, title string, o *wiCreateOpts) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	node, err := createNode(p, typeName, title, o)
	if err != nil {
		return fmt.Errorf("create %s: %w", typeName, err)
	}

	warnMissingFields(typeName, o)

	if typeName == "bug" && !o.noLink {
		if featID := detectActiveFeature(p, dir); featID != "" {
			autoCausedByEdge(p, node.ID, featID)
			fmt.Printf("  (linked to %s)\n", featID)
		}
	}

	if o.trackID != "" && typeName != "track" {
		if linkErr := autoTrackEdges(p, node.ID, typeName, o.trackID, node.Title); linkErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: auto-link to track failed: %v\n", linkErr)
		}
	}

	if o.start {
		if _, startErr := collectionFor(p, typeName).Start(node.ID); startErr != nil {
			return fmt.Errorf("start %s: %w", typeName, startErr)
		}
		fmt.Printf("Created and started: %s  %s\n", node.ID, node.Title)
	} else {
		fmt.Printf("Created: %s  %s\n", node.ID, node.Title)
	}
	return nil
}

func wiListCmd(typeName, dirName string) *cobra.Command {
	var statusFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List " + dirName,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runWiList(dirName, statusFilter)
		},
	}
	cmd.Flags().StringVarP(&statusFilter, "status", "s", "",
		"Filter by status (todo, in-progress, blocked, done)")
	return cmd
}

func runWiList(dirName, statusFilter string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	nodes, err := graph.LoadDir(filepath.Join(dir, dirName))
	if err != nil {
		return fmt.Errorf("load %s: %w", dirName, err)
	}

	var filtered []*models.Node
	for _, n := range nodes {
		if statusFilter != "" && string(n.Status) != statusFilter {
			continue
		}
		filtered = append(filtered, n)
	}

	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })

	if len(filtered) == 0 {
		fmt.Printf("No %s found.\n", dirName)
		return nil
	}

	fmt.Printf("%-22s  %-11s  %-8s  %s\n", "ID", "STATUS", "PRIORITY", "TITLE")
	fmt.Println(strings.Repeat("-", 80))
	for _, n := range filtered {
		marker := "  "
		if n.Status == models.StatusInProgress {
			marker = "* "
		}
		fmt.Printf("%s%-20s  %-11s  %-8s  %s\n",
			marker, n.ID, n.Status, n.Priority, truncate(n.Title, 44))
	}
	fmt.Printf("\n%d %s\n", len(filtered), dirName)
	return nil
}

func wiShowCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show " + typeName + " details",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return runWiShow(args[0]) },
	}
}

func runWiShow(id string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	path := resolveNodePath(dir, id)
	if path == "" {
		return fmt.Errorf("work item %q not found", id)
	}
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	printNodeDetail(node)
	return nil
}

func wiStartCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <id>",
		Short: "Mark a " + typeName + " as in-progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiSetStatus(typeName, args[0], "in-progress")
		},
	}
}

func wiCompleteCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "complete <id>",
		Short: "Mark a " + typeName + " as done",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiSetStatus(typeName, args[0], "done")
		},
	}
}

func runWiSetStatus(typeName, id, status string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	var node *models.Node
	if status == "in-progress" {
		node, err = col.Start(id)
	} else {
		node, err = col.Complete(id)
	}
	if err != nil {
		return fmt.Errorf("set %s %s: %w", typeName, status, err)
	}

	// When starting a work item, update active_feature_id and create
	// an implemented_in edge linking the work item to this session.
	if status == "in-progress" {
		sessionID := hooks.EnvSessionID("")
		if sessionID != "" {
			if p.DB != nil {
				_ = hooks.UpdateActiveFeature(p.DB, sessionID, id)
			}
			// Auto-create implemented_in edge (idempotent — skip if exists).
			autoImplementedInEdge(col, id, sessionID)
		}
	}

	verb := "Started"
	if status == "done" {
		verb = "Completed"
	}
	fmt.Printf("%s: %s  %s\n", verb, node.ID, node.Title)
	return nil
}

func collectionFor(p *workitem.Project, typeName string) *workitem.Collection {
	switch typeName {
	case "bug":
		return p.Bugs.Collection
	case "spike":
		return p.Spikes.Collection
	case "track":
		return p.Tracks.Collection
	case "plan":
		return p.Plans.Collection
	case "spec":
		return p.Specs.Collection
	default:
		return p.Features.Collection
	}
}

func wiDeleteCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a " + typeName,
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return runWiDelete(args[0]) },
	}
}

func runWiDelete(id string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	path := resolveNodePath(dir, id)
	if path == "" {
		return fmt.Errorf("work item %q not found", id)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	fmt.Printf("Deleted: %s\n", id)
	return nil
}
