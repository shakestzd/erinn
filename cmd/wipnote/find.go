package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// findProjectOpener is the function used to open the workitem project. It is a
// package-level variable so tests can inject a spy to assert that the DB is
// never opened for find queries (canonical-first guarantee).
//
// In production this variable is never invoked because the canonical path
// (graph.LoadAll / graph.LoadDir) does not call workitem.Open. The variable
// exists solely so a test spy can detect any accidental DB access regression.
var findProjectOpener = workitem.Open //nolint:unused

// workItemIDPattern matches canonical work item IDs like feat-abc12345, bug-abc12345, etc.
var workItemIDPattern = regexp.MustCompile(`^(feat|bug|spk|trk|plan|pln|spec|spc)-[0-9a-f]{8}$`)

// knownCollections is the set of valid collection names for find.
var knownCollections = map[string]bool{
	"features": true,
	"bugs":     true,
	"spikes":   true,
	"tracks":   true,
	"plans":    true,
	"specs":    true,
	"all":      true,
}

func findCmd() *cobra.Command {
	var (
		status   string
		priority string
		title    string
		trackID  string
		agent    string
		orderBy  string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "find <collection>",
		Short: "Query work items with filters",
		Long: `Search across collections using composable filters.

Collections: features, bugs, spikes, tracks, plans, specs, all

Examples:
  wipnote find features --status blocked
  wipnote find bugs --priority high --status todo
  wipnote find all --status in-progress --order-by created
  wipnote find features --title "auth" --limit 5`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runFind(args[0], findOpts{
				status:   status,
				priority: priority,
				title:    title,
				trackID:  trackID,
				agent:    agent,
				orderBy:  orderBy,
				limit:    limit,
			})
		},
	}

	cmd.Flags().StringVarP(&status, "status", "s", "",
		"Filter by status (todo, in-progress, blocked, done)")
	cmd.Flags().StringVarP(&priority, "priority", "p", "",
		"Filter by priority (low, medium, high, critical)")
	cmd.Flags().StringVarP(&title, "title", "t", "",
		"Filter by title substring (case-insensitive)")
	cmd.Flags().StringVar(&trackID, "track", "",
		"Filter by track ID")
	cmd.Flags().StringVar(&agent, "agent", "",
		"Filter by assigned agent")
	cmd.Flags().StringVar(&orderBy, "order-by", "",
		"Sort field: created, updated, title, priority, id")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0,
		"Maximum number of results")

	return cmd
}

// findOpts holds parsed CLI flags for the find command.
type findOpts struct {
	status   string
	priority string
	title    string
	trackID  string
	agent    string
	orderBy  string
	limit    int
}

// runFind implements canonical-first find: all data is read from .wipnote/*.html
// files; SQLite is never opened. If the argument looks like a work item ID, a
// direct HTML lookup is performed. Otherwise, the argument is treated as a
// collection name (or title search when not a known collection).
func runFind(collection string, opts findOpts) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	// If the argument looks like a work item ID, do a direct lookup.
	if workItemIDPattern.MatchString(collection) {
		return runFindByID(dir, collection)
	}

	// If the argument is not a known collection name, treat it as a title search.
	if !knownCollections[collection] {
		opts.title = collection
		collection = "all"
	}

	// Canonical-first: load nodes directly from HTML files — no SQLite needed.
	nodes, err := loadFindNodes(dir, collection)
	if err != nil {
		return fmt.Errorf("find: %w", err)
	}

	// Apply filters in memory.
	nodes = applyFindFilters(nodes, opts)

	// Apply ordering.
	if opts.orderBy != "" {
		sortFindNodes(nodes, opts.orderBy)
	}

	// Apply limit.
	if opts.limit > 0 && len(nodes) > opts.limit {
		nodes = nodes[:opts.limit]
	}

	if len(nodes) == 0 {
		fmt.Println("No matching items found.")
		return nil
	}

	printFindResults(nodes)
	return nil
}

// loadFindNodes loads nodes from HTML files for the given collection, merged
// with any archived rows for that collection. collection == "all" loads across
// all standard collections (LoadAll is already archive-aware). Scoped lookups
// (e.g. `find features <q>`) merge live files with the collection's archive
// ledger so archived items remain findable; live files win on ID collisions.
//
// NOTE: this is the canonical `find` read path, NOT `feature list`. The default
// `wipnote <type> list` (runWiList) deliberately stays file-only / curated-active
// and does NOT surface archived items — that decluttering is the archive feature's
// purpose.
func loadFindNodes(wipnoteDir, collection string) ([]*models.Node, error) {
	if collection == "all" {
		return graph.LoadAll(wipnoteDir)
	}
	dir := filepath.Join(wipnoteDir, collection)
	nodes, err := graph.LoadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", collection, err)
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		seen[n.ID] = true
	}
	archived, err := graph.LoadArchivedNodesForCollection(wipnoteDir, collection)
	if err != nil {
		return nil, fmt.Errorf("load archived %s: %w", collection, err)
	}
	for _, n := range archived {
		if !seen[n.ID] {
			seen[n.ID] = true
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

// applyFindFilters returns the subset of nodes that match all active filters.
func applyFindFilters(nodes []*models.Node, opts findOpts) []*models.Node {
	if opts.status == "" && opts.priority == "" && opts.title == "" &&
		opts.trackID == "" && opts.agent == "" {
		return nodes
	}
	titleLower := strings.ToLower(opts.title)
	out := nodes[:0:0]
	for _, n := range nodes {
		if opts.status != "" && string(n.Status) != opts.status {
			continue
		}
		if opts.priority != "" && string(n.Priority) != opts.priority {
			continue
		}
		if opts.title != "" && !strings.Contains(strings.ToLower(n.Title), titleLower) {
			continue
		}
		if opts.trackID != "" && n.TrackID != opts.trackID {
			continue
		}
		if opts.agent != "" && n.AgentAssigned != opts.agent {
			continue
		}
		out = append(out, n)
	}
	return out
}

// sortFindNodes sorts nodes in-place by field (ascending).
func sortFindNodes(nodes []*models.Node, field string) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		switch strings.ToLower(field) {
		case "created", "created_at":
			return a.CreatedAt.Before(b.CreatedAt)
		case "updated", "updated_at":
			return a.UpdatedAt.Before(b.UpdatedAt)
		case "title":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "priority":
			return priorityRank(a.Priority) < priorityRank(b.Priority)
		default: // "id" and unknown fields
			return a.ID < b.ID
		}
	})
}

// priorityRank maps priority to a sortable int (higher = more urgent).
func priorityRank(p models.Priority) int {
	switch p {
	case models.PriorityLow:
		return 0
	case models.PriorityMedium:
		return 1
	case models.PriorityHigh:
		return 2
	case models.PriorityCritical:
		return 3
	default:
		return -1
	}
}

// runFindByID resolves a work item by its canonical ID and prints it.
func runFindByID(dir, id string) error {
	path := resolveNodePath(dir, id)
	if path == "" {
		// Fall back to the archive ledgers before declaring the item missing —
		// archived items have no individual file but remain queryable by ID.
		archived, archErr := resolveArchivedNode(dir, id)
		if archErr != nil {
			return archErr
		}
		if archived != nil {
			printFindResults([]*models.Node{archived})
			return nil
		}
		kind := kindFromPrefix(id)
		return fmt.Errorf("find: no item found with ID %q\nRun 'wipnote %s list' to see valid IDs, or 'wipnote find all --title <keyword>' to search by title", id, kind)
	}
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	printFindResults([]*models.Node{node})
	return nil
}

func printFindResults(nodes []*models.Node) {
	fmt.Printf("%-22s  %-8s  %-11s  %-8s  %s\n",
		"ID", "TYPE", "STATUS", "PRIORITY", "TITLE")
	fmt.Println(strings.Repeat("-", 80))

	for _, n := range nodes {
		marker := "  "
		if n.Status == models.StatusInProgress {
			marker = "* "
		}
		fmt.Printf("%s%-20s  %-8s  %-11s  %-8s  %s\n",
			marker, n.ID, n.Type, n.Status, n.Priority,
			truncate(n.Title, 36))
	}

	fmt.Printf("\n%d item(s)\n", len(nodes))
}
