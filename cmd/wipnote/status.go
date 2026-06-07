package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/storage"
	versionpkg "github.com/shakestzd/wipnote/internal/version"
	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show work item status summary",
		RunE:  runStatus,
	}
}

func runStatus(_ *cobra.Command, _ []string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	nodes, err := graph.LoadAll(dir)
	if err != nil {
		return fmt.Errorf("load work items: %w", err)
	}

	// Group by type then status.
	type counts struct {
		todo, inProgress, blocked, done, other int
	}
	byType := make(map[string]*counts)
	var inProgress []*models.Node

	for _, n := range nodes {
		if byType[n.Type] == nil {
			byType[n.Type] = &counts{}
		}
		c := byType[n.Type]
		switch n.Status {
		case models.StatusTodo:
			c.todo++
		case models.StatusInProgress:
			c.inProgress++
			inProgress = append(inProgress, n)
		case models.StatusBlocked:
			c.blocked++
		case models.StatusDone:
			c.done++
		default:
			c.other++
		}
	}

	fmt.Printf("wipnote status  (%s)\n\n", dir)

	types := []string{"feature", "bug", "spike", "track"}
	for _, t := range types {
		c := byType[t]
		if c == nil {
			continue
		}
		total := c.todo + c.inProgress + c.blocked + c.done + c.other
		fmt.Printf("  %-10s  %d total  (todo:%d  active:%d  blocked:%d  done:%d)\n",
			t+"s", total, c.todo, c.inProgress, c.blocked, c.done)
	}

	if len(inProgress) > 0 {
		sort.Slice(inProgress, func(i, j int) bool {
			return inProgress[i].ID < inProgress[j].ID
		})
		fmt.Println("\nIn progress:")
		for _, n := range inProgress {
			fmt.Printf("  %-20s  %s\n", n.ID, truncate(n.Title, 60))
		}
	}

	// DB path, filesystem type, journal_mode, and attribution diagnostics.
	if dbInfo, pathErr := storage.CanonicalDBPathWithInfo(filepath.Dir(dir)); pathErr == nil {
		fmt.Printf("\nDB: %s\n", dbInfo.Path)
		fmt.Printf("  fstype=%s  reason: %s\n", dbInfo.FsType, dbInfo.Reason)
		if database, dbErr := dbpkg.Open(dbInfo.Path); dbErr == nil {
			defer database.Close()
			jm := dbpkg.QueryJournalMode(database)
			fmt.Printf("  journal_mode=%s\n", jm)
			total, attributed := dbpkg.CommitAttributionRate(database)
			if total > 0 {
				pct := float64(attributed) / float64(total) * 100
				fmt.Printf("  attribution: %d/%d commits (%.1f%%)\n", attributed, total, pct)
			}
		}
	}

	// Slice-10 contention observability: surface the in-process BUSY
	// counters + writer queue depth so operators can see contention at
	// a glance. The counters are first-party-only (per the slice-5
	// boundary); external producers like MCP are excluded from the
	// launch criterion.
	printContentionStatus()

	fmt.Printf("\nTotal: %d work items\n", len(nodes))

	// Collector health — scan .wipnote/sessions/ for recent PID files.
	printCollectorHealth(filepath.Dir(dir))

	if latest, newer, _ := versionpkg.CheckForUpdate(version); newer {
		fmt.Printf("\nUpdate available: v%s (current: v%s)\n", latest, version)
	}

	return nil
}

// printContentionStatus prints the slice-10 SQLITE_BUSY counter snapshot
// plus the slice-6 writer queue depth (when wired in this process). The
// counters are process-local — in a long-running `wipnote serve` they
// reflect cumulative contention since startup; in a short-lived CLI
// invocation they only reflect the current process. Either is useful
// for the launch readiness check.
func printContentionStatus() {
	counts := dbpkg.BusyCounts()
	firstPartyTotal := dbpkg.FirstPartyBusyTotal()

	// Always print the header line so the absence of contention is itself
	// a visible signal — "0 SQLITE_BUSY" beats silent omission for the
	// launch gate.
	fmt.Printf("\nContention: %d SQLITE_BUSY (first-party)", firstPartyTotal)
	if len(counts) == 0 {
		fmt.Println("  [no contention recorded]")
	} else {
		fmt.Println()
		// Stable subsystem order: first-party labels first, then external.
		ordered := []dbpkg.BusySubsystem{
			dbpkg.SubsystemHookWriter,
			dbpkg.SubsystemIndexer,
			dbpkg.SubsystemCLIMutation,
			dbpkg.SubsystemWriterService,
			dbpkg.SubsystemExternal,
		}
		for _, s := range ordered {
			if c, ok := counts[s]; ok && c > 0 {
				fmt.Printf("  %-16s %d\n", string(s), c)
			}
		}
	}

	// Writer queue depth — only meaningful when a writer service is
	// constructed (i.e., inside `wipnote serve`); short-lived CLI
	// invocations of status will see writerService.queue == nil and we
	// print "disabled" so operators know it's not a missing-feature
	// problem.
	queueStatus := readWriterServiceStatus(writerService.queue)
	fmt.Printf("Writer queue: state=%s depth=%d/%d enq=%d deq=%d rej=%d err=%d\n",
		queueStatus.State, queueStatus.Depth, queueStatus.Capacity,
		queueStatus.Enqueued, queueStatus.Dequeued,
		queueStatus.Rejected, queueStatus.Errors)
}

// printCollectorHealth lists per-session collector status for any session
// directory that contains a .collector-pid file. Best-effort: silently skips
// sessions where the PID file is missing or unreadable.
func printCollectorHealth(projectDir string) {
	sessionsDir := filepath.Join(projectDir, ".wipnote", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return // no sessions dir yet — nothing to print
	}

	printed := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessDir := filepath.Join(sessionsDir, e.Name())
		pidPath := filepath.Join(sessDir, ".collector-pid")
		if _, statErr := os.Stat(pidPath); statErr != nil {
			continue // no PID file — collector was never spawned for this session
		}
		cs, err := ReadCollectorStatus(sessDir)
		if err != nil {
			continue
		}
		if !printed {
			fmt.Println("\nCollector health:")
			printed = true
		}
		if cs.Alive {
			fmt.Printf("  %-36s  pid=%-7d port=%-5d alive=true  uptime=%ds\n",
				e.Name(), cs.PID, cs.Port, cs.UptimeSec)
		} else {
			fmt.Printf("  %-36s  pid=%-7d port=%-5d alive=false last_seen=%d\n",
				e.Name(), cs.PID, cs.Port, cs.LastActivityMs)
		}
	}
}
