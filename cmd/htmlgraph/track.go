package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

// trackCmdWithExtras builds the standard workitem commands for tracks,
// then replaces the generic show with a track-specific one that lists
// all linked children (features, bugs, and spikes).
func trackCmdWithExtras() *cobra.Command {
	cmd := workitemCmd("track", "tracks")
	// Replace generic show with track-specific show (shows linked features)
	for i, sub := range cmd.Commands() {
		if sub.Use == "show <id>" {
			cmd.RemoveCommand(sub)
			newCmds := append(cmd.Commands()[:i], cmd.Commands()[i:]...)
			_ = newCmds // removal already happened
			break
		}
	}
	cmd.AddCommand(trackShowCmd())
	cmd.AddCommand(trackStatusCmd())
	cmd.AddCommand(trackPRCmd())
	return cmd
}


// trackShowCmd shows a single track by ID.
func trackShowCmd() *cobra.Command {
	var deep bool
	var format string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show track details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTrackShowWithFormat(args[0], deep, format)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Show all linked items with steps and edges")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: json or text")
	return cmd
}

func runTrackShow(id string, deep bool) error {
	return runTrackShowWithFormat(id, deep, "text")
}

// runTrackShowWithFormat shows a track in the requested format (text or json).
func runTrackShowWithFormat(id string, deep bool, format string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	// Try flat format first: tracks/id.html
	path := filepath.Join(dir, "tracks", id+".html")
	if _, err := os.Stat(path); err != nil {
		// Try subdirectory format: tracks/id/index.html
		path = filepath.Join(dir, "tracks", id, "index.html")
		if _, err := os.Stat(path); err != nil {
			return workitem.ErrNotFound("track", id)
		}
	}

	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	switch format {
	case "json":
		return printNodeDetailJSON(node)
	default:
		if deep {
			printTrackDeep(node, dir)
		} else {
			printTrackDetail(node, dir)
		}
		return nil
	}
}

// topoSortFeatures returns features sorted by dependency order using blocked_by
// edges. Features with no dependencies come first (Kahn's BFS algorithm).
func topoSortFeatures(features []*models.Node) []*models.Node {
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)
	nodeMap := make(map[string]*models.Node)

	for _, f := range features {
		nodeMap[f.ID] = f
		inDegree[f.ID] = 0
	}

	for _, f := range features {
		for _, e := range f.Edges[string(models.RelBlockedBy)] {
			if _, ok := nodeMap[e.TargetID]; ok {
				inDegree[f.ID]++
				dependents[e.TargetID] = append(dependents[e.TargetID], f.ID)
			}
		}
	}

	var queue []string
	for _, f := range features {
		if inDegree[f.ID] == 0 {
			queue = append(queue, f.ID)
		}
	}
	// Stable ordering for nodes at the same level.
	sort.Strings(queue)

	var sorted []*models.Node
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, nodeMap[id])
		deps := dependents[id]
		sort.Strings(deps)
		for _, dep := range deps {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	// Append remaining nodes (cycles or disconnected).
	seen := make(map[string]bool, len(sorted))
	for _, n := range sorted {
		seen[n.ID] = true
	}
	for _, f := range features {
		if !seen[f.ID] {
			sorted = append(sorted, f)
		}
	}
	return sorted
}

// statusMarker returns the display glyph for a feature node.
// isNextActionable is true when the feature is todo/in-progress with all
// blocked_by targets resolved as done.
func statusMarker(n *models.Node, isNextActionable bool) string {
	switch n.Status {
	case models.StatusDone:
		return "✓"
	case models.StatusInProgress:
		return "*"
	default:
		if isNextActionable {
			return "→"
		}
		return "○"
	}
}

// isActionable reports whether a feature has no unresolved blockers.
func isActionable(f *models.Node, nodeMap map[string]*models.Node) bool {
	if f.Status == models.StatusDone || f.Status == models.StatusInProgress {
		return false
	}
	for _, e := range f.Edges[string(models.RelBlockedBy)] {
		blocker, ok := nodeMap[e.TargetID]
		if !ok || blocker.Status != models.StatusDone {
			return false
		}
	}
	return true
}

// openTrackDB opens the SQLite DB if it exists; returns nil without error when
// the file is absent (file counts are optional).
func openTrackDB(htmlgraphDir string) *sql.DB {
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	database, err := db.Open(dbPath)
	if err != nil {
		return nil
	}
	return database
}

// fileCountForFeature queries feature_files for a distinct file count.
func fileCountForFeature(database *sql.DB, featureID string) int {
	if database == nil {
		return 0
	}
	var count int
	database.QueryRow(
		"SELECT COUNT(DISTINCT file_path) FROM feature_files WHERE feature_id = ?",
		featureID,
	).Scan(&count)
	return count
}

// printFeatureRow renders one feature in the rich track display.
func printFeatureRow(f *models.Node, marker string, nodeMap map[string]*models.Node, database *sql.DB) {
	fmt.Printf("  %s %s  %s\n", marker, f.ID, f.Title)

	// blocked_by line — only show blockers that are part of the track.
	blockers := f.Edges[string(models.RelBlockedBy)]
	if len(blockers) > 0 {
		parts := make([]string, 0, len(blockers))
		for _, e := range blockers {
			if blocker, ok := nodeMap[e.TargetID]; ok {
				bmark := "○"
				if blocker.Status == models.StatusDone {
					bmark = "✓"
				}
				parts = append(parts, e.TargetID+" "+bmark)
			}
		}
		if len(parts) > 0 {
			fmt.Printf("      blocked by: %s\n", strings.Join(parts, ", "))
		}
	}

	// Description — first 80 chars of Content.
	if f.Content != "" {
		desc := strings.TrimSpace(strings.Split(f.Content, "\n")[0])
		fmt.Printf("      %s\n", truncate(desc, 80))
	}

	// Step count + file count.
	if len(f.Steps) > 0 {
		done := 0
		for _, s := range f.Steps {
			if s.Completed {
				done++
			}
		}
		fc := fileCountForFeature(database, f.ID)
		if fc > 0 {
			fmt.Printf("      %d steps (%d/%d) | %d files\n", len(f.Steps), done, len(f.Steps), fc)
		} else {
			fmt.Printf("      %d steps (%d/%d)\n", len(f.Steps), done, len(f.Steps))
		}
	} else {
		fc := fileCountForFeature(database, f.ID)
		if fc > 0 {
			fmt.Printf("      %d files\n", fc)
		}
	}
}

func printTrackDetail(n *models.Node, htmlgraphDir string) {
	features := loadLinkedByType(htmlgraphDir, "features", n.ID)
	sorted := topoSortFeatures(features)

	doneCount := 0
	for _, f := range features {
		if f.Status == models.StatusDone {
			doneCount++
		}
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  %s\n", n.Title)
	fmt.Println(sep)
	fmt.Printf("  ID        %s\n", n.ID)
	fmt.Printf("  Type      %s\n", n.Type)
	if len(features) > 0 {
		fmt.Printf("  Status    %s (%d/%d done)\n", n.Status, doneCount, len(features))
	} else {
		fmt.Printf("  Status    %s\n", n.Status)
	}
	fmt.Printf("  Priority  %s\n", n.Priority)
	if !n.CreatedAt.IsZero() {
		fmt.Printf("  Created   %s\n", n.CreatedAt.Format("2006-01-02"))
	}

	if len(sorted) > 0 {
		database := openTrackDB(htmlgraphDir)
		if database != nil {
			defer database.Close()
		}

		nodeMap := make(map[string]*models.Node, len(sorted))
		for _, f := range sorted {
			nodeMap[f.ID] = f
		}

		fmt.Printf("\nFeatures (dependency order):\n")
		for _, f := range sorted {
			actionable := isActionable(f, nodeMap)
			marker := statusMarker(f, actionable)
			printFeatureRow(f, marker, nodeMap, database)
		}
	}

	printLinkedSection(htmlgraphDir, "bugs", "Linked bugs", n.ID)
	printLinkedSection(htmlgraphDir, "spikes", "Linked spikes", n.ID)

	if n.Content != "" {
		fmt.Println("\nDescription:")
		for _, line := range strings.Split(n.Content, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}

	if len(n.Steps) > 0 {
		done := 0
		for _, s := range n.Steps {
			if s.Completed {
				done++
			}
		}
		fmt.Printf("\nRequirements: %d/%d complete\n", done, len(n.Steps))
		for _, s := range n.Steps {
			tick := "[ ]"
			if s.Completed {
				tick = "[x]"
			}
			fmt.Printf("  %s  %s\n", tick, s.Description)
		}
	}
}

// printLinkedSection prints a labelled section of items linked to a track,
// covering a single work item subdir (features, bugs, or spikes).
func printLinkedSection(htmlgraphDir, subdir, label, trackID string) {
	items := loadLinkedByType(htmlgraphDir, subdir, trackID)
	if len(items) == 0 {
		return
	}
	fmt.Printf("\n%s (%d):\n", label, len(items))
	for _, item := range items {
		marker := "  "
		if item.Status == models.StatusInProgress {
			marker = "* "
		}
		fmt.Printf("  %s%-20s  %-11s  %s\n",
			marker, item.ID, item.Status, truncate(item.Title, 38))
	}
}

// containsEdgeIDs returns the set of target IDs referenced by a track's
// "contains" edges, so loadLinkedByType can include edge-linked children that
// do not carry the data-track-id attribute.
func containsEdgeIDs(htmlgraphDir, trackID string) map[string]bool {
	path := filepath.Join(htmlgraphDir, "tracks", trackID+".html")
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return nil
	}
	ids := make(map[string]bool)
	for _, e := range node.Edges[string(models.RelContains)] {
		ids[e.TargetID] = true
	}
	return ids
}

// loadLinkedByType returns nodes of a given subdir linked to trackID either
// via the TrackID metadata field or via a "contains" edge on the track.
func loadLinkedByType(htmlgraphDir, subdir, trackID string) []*models.Node {
	nodes, err := graph.LoadDir(filepath.Join(htmlgraphDir, subdir))
	if err != nil {
		return nil
	}
	edgeIDs := containsEdgeIDs(htmlgraphDir, trackID)
	seen := make(map[string]bool)
	var linked []*models.Node
	for _, n := range nodes {
		if seen[n.ID] {
			continue
		}
		if n.TrackID == trackID || edgeIDs[n.ID] {
			linked = append(linked, n)
			seen[n.ID] = true
		}
	}
	sort.Slice(linked, func(i, j int) bool {
		return linked[i].ID < linked[j].ID
	})
	return linked
}

// printItemSteps prints indented step checklist for an item.
func printItemSteps(n *models.Node) {
	done := 0
	for _, s := range n.Steps {
		if s.Completed {
			done++
		}
	}
	fmt.Printf("    Steps: %d/%d complete\n", done, len(n.Steps))
	for _, s := range n.Steps {
		tick := "[ ]"
		if s.Completed {
			tick = "[x]"
		}
		fmt.Printf("      %s %s\n", tick, truncate(s.Description, 60))
	}
}

// printItemEdges prints indented edges for an item, skipping part_of.
func printItemEdges(n *models.Node) {
	if len(n.Edges) == 0 {
		return
	}
	fmt.Println("    Edges:")
	for rel, edges := range n.Edges {
		if rel == "part_of" {
			continue
		}
		for _, e := range edges {
			fmt.Printf("      %s -> %s\n", rel, e.TargetID)
		}
	}
}

// printDeepItem prints a single linked item with steps and edges.
func printDeepItem(n *models.Node) {
	var marker string
	switch n.Status {
	case models.StatusInProgress:
		marker = "* "
	case models.StatusDone:
		marker = "✓ "
	default:
		marker = "  "
	}
	fmt.Printf("  %s%-20s  %-11s  %s\n", marker, n.ID, n.Status, truncate(n.Title, 38))
	if len(n.Steps) > 0 {
		printItemSteps(n)
	}
	printItemEdges(n)
}

// printDeepGroup prints a group of linked items by type label.
func printDeepGroup(label string, items []*models.Node) {
	fmt.Printf("\n%s (%d):\n", label, len(items))
	if len(items) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, n := range items {
		printDeepItem(n)
	}
}

// printTrackDeep prints a track with all linked items (features, bugs, spikes).
func printTrackDeep(n *models.Node, htmlgraphDir string) {
	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  %s\n", n.Title)
	fmt.Println(sep)
	fmt.Printf("  ID        %s\n", n.ID)
	fmt.Printf("  Status    %s\n", n.Status)
	fmt.Printf("  Priority  %s\n", n.Priority)
	if !n.CreatedAt.IsZero() {
		fmt.Printf("  Created   %s\n", n.CreatedAt.Format("2006-01-02"))
	}
	features := loadLinkedByType(htmlgraphDir, "features", n.ID)
	bugs := loadLinkedByType(htmlgraphDir, "bugs", n.ID)
	spikes := loadLinkedByType(htmlgraphDir, "spikes", n.ID)
	printDeepGroup("Features", features)
	printDeepGroup("Bugs", bugs)
	printDeepGroup("Spikes", spikes)
}
