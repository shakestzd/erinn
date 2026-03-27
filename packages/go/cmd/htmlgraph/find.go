package main

import (
	"fmt"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/pkg/sdk"
	"github.com/spf13/cobra"
)

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

Collections: features, bugs, spikes, tracks, all

Examples:
  htmlgraph find features --status blocked
  htmlgraph find bugs --priority high --status todo
  htmlgraph find all --status in-progress --order-by created
  htmlgraph find features --title "auth" --limit 5`,
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

func runFind(collection string, opts findOpts) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	s, err := sdk.New(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open SDK: %w", err)
	}
	defer s.Close()

	// Build query.
	var q *sdk.Query
	if collection == "all" {
		q = s.FindAll()
	} else {
		q = s.Find(collection)
	}

	// Apply filters.
	if opts.status != "" {
		q = q.Where(sdk.StatusIs(opts.status))
	}
	if opts.priority != "" {
		q = q.Where(sdk.PriorityIs(opts.priority))
	}
	if opts.title != "" {
		q = q.Where(sdk.TitleContains(opts.title))
	}
	if opts.trackID != "" {
		q = q.Where(sdk.TrackIs(opts.trackID))
	}
	if opts.agent != "" {
		q = q.Where(sdk.AgentIs(opts.agent))
	}

	// Apply ordering.
	if opts.orderBy != "" {
		q = q.OrderBy(opts.orderBy, sdk.Asc)
	}

	// Apply limit.
	if opts.limit > 0 {
		q = q.Limit(opts.limit)
	}

	nodes, err := q.Execute()
	if err != nil {
		return fmt.Errorf("find: %w", err)
	}

	if len(nodes) == 0 {
		fmt.Println("No matching items found.")
		return nil
	}

	printFindResults(nodes)
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
