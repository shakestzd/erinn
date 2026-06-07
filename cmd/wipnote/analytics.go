package main

// Register in main.go: rootCmd.AddCommand(analyticsCmd())

import (
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func analyticsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Project analytics and planning insights",
	}
	cmd.AddCommand(analyticsSummaryCmd())
	cmd.AddCommand(analyticsVelocityCmd())
	return cmd
}

// analyticsSummaryCmd shows work type distribution and maintenance burden.
func analyticsSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Work type distribution and maintenance burden",
		RunE:  runAnalyticsSummary,
	}
}

// statusCounts holds item counts per lifecycle state for a single work type.
type statusCounts struct{ todo, active, blocked, done int }

func runAnalyticsSummary(_ *cobra.Command, _ []string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	totals := make(map[string]*statusCounts)

	for _, n := range nodes {
		if totals[n.Type] == nil {
			totals[n.Type] = &statusCounts{}
		}
		c := totals[n.Type]
		switch n.Status {
		case models.StatusTodo:
			c.todo++
		case models.StatusInProgress:
			c.active++
		case models.StatusBlocked:
			c.blocked++
		case models.StatusDone:
			c.done++
		}
	}

	fmt.Printf("Work type distribution  (%s)\n\n", dir)
	fmt.Printf("%-10s  %5s  %6s  %7s  %4s\n", "TYPE", "TODO", "ACTIVE", "BLOCKED", "DONE")
	fmt.Println("────────────────────────────────────────")

	typeOrder := []string{"feature", "bug", "spike", "track"}
	for _, t := range typeOrder {
		c := totals[t]
		if c == nil {
			continue
		}
		total := c.todo + c.active + c.blocked + c.done
		fmt.Printf("%-10s  %5d  %6d  %7d  %4d  (total: %d)\n",
			t+"s", c.todo, c.active, c.blocked, c.done, total)
	}

	fmt.Println("────────────────────────────────────────")
	fmt.Printf("%-10s  %5d\n\n", "total", len(nodes))

	printMaintenanceBurden(totals)
	return nil
}

func printMaintenanceBurden(totals map[string]*statusCounts) {
	cf := totals["feature"]
	cb := totals["bug"]
	if cf == nil || cb == nil {
		return
	}

	featureTotal := cf.todo + cf.active + cf.blocked + cf.done
	bugTotal := cb.todo + cb.active + cb.blocked + cb.done
	if featureTotal == 0 {
		return
	}

	ratio := float64(bugTotal) / float64(featureTotal)
	label := "healthy"
	if ratio > 0.5 {
		label = "high — consider a bug-reduction sprint"
	} else if ratio > 0.3 {
		label = "moderate"
	}

	fmt.Printf("Maintenance burden: %.1f bugs per feature — %s\n", ratio, label)
}

// analyticsVelocityCmd counts items completed per week for the last 4 weeks.
func analyticsVelocityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "velocity",
		Short: "Items completed per week (last 4 weeks)",
		RunE:  runAnalyticsVelocity,
	}
}

func runAnalyticsVelocity(_ *cobra.Command, _ []string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	weeks := velocityBuckets(nodes, 4)

	fmt.Printf("Velocity — items completed per week  (%s)\n\n", dir)
	fmt.Printf("%-14s  %5s\n", "WEEK", "DONE")
	fmt.Println("────────────────────────")

	for _, w := range weeks {
		bar := progressBar(w.count, 20)
		fmt.Printf("%-14s  %5d  %s\n", w.label, w.count, bar)
	}
	return nil
}

type weekBucket struct {
	label string
	count int
}

func velocityBuckets(nodes []*models.Node, numWeeks int) []weekBucket {
	now := time.Now().UTC()
	buckets := make([]weekBucket, numWeeks)

	for i := range buckets {
		weekStart := now.AddDate(0, 0, -(numWeeks-i)*7)
		weekEnd := weekStart.AddDate(0, 0, 7)
		buckets[i].label = weekStart.Format("2006-01-02")
		for _, n := range nodes {
			if n.Status != models.StatusDone {
				continue
			}
			if n.UpdatedAt.After(weekStart) && !n.UpdatedAt.After(weekEnd) {
				buckets[i].count++
			}
		}
	}
	return buckets
}

func progressBar(count, maxWidth int) string {
	if count == 0 {
		return ""
	}
	width := count
	if width > maxWidth {
		width = maxWidth
	}
	bar := make([]rune, width)
	for i := range bar {
		bar[i] = '█'
	}
	return string(bar)
}
