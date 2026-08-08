package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// defaultClaimArchiveAgeDays mirrors work-item archiving's threshold shape: a
// closed session's shard stays an individual file for a while, where it is
// cheapest to read and to diff, before being compacted.
const defaultClaimArchiveAgeDays = 14

// claimsCmd is the read/maintenance surface over the durable claim ledger.
//
// It is deliberately separate from the existing `wipnote claim` command, which
// operates on the SQLite claims table (current state, leases, heartbeats). This
// one operates on claim HISTORY: intervals that outlive the session, the cache,
// and the clone.
func claimsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "Query and maintain the durable claim-episode ledger",
		Long: `The claim ledger records which agent held which work item over which
interval. Unlike 'wipnote claim' (current leases in the read index), these rows
are canonical HTML under .wipnote/claims/, git-tracked, and survive a cache wipe
and a fresh clone.

One row per claim EPISODE: work item, session, agent, start, end, outcome.
Heartbeat renewals record nothing.`,
	}
	cmd.AddCommand(claimsWhoCmd())
	cmd.AddCommand(claimsListCmd())
	cmd.AddCommand(claimsReconcileCmd())
	cmd.AddCommand(claimsArchiveCmd())
	return cmd
}

// claimsWhoCmd answers the query the ledger exists for.
func claimsWhoCmd() *cobra.Command {
	var agentID, sessionID, atStr string
	cmd := &cobra.Command{
		Use:   "who",
		Short: "Resolve the work item an agent held at a point in time",
		Long: `Given an agent and a timestamp, print the work item that agent held then.

This is the join that per-signal attribution needs: a signal knows its agent and
its time, and this returns what to charge it to.

With no --agent, prints every episode covering the instant.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			at := time.Now().UTC()
			if atStr != "" {
				parsed, err := claimledger.ParseTime(atStr)
				if err != nil {
					return fmt.Errorf("parse --at %q: %w (want RFC3339, e.g. 2026-08-08T10:00:00Z)", atStr, err)
				}
				at = parsed
			}

			dir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			database, err := openDB(dir)
			if err != nil {
				return err
			}
			defer database.Close()

			if agentID != "" {
				workItem, qErr := dbpkg.WorkItemForSessionAgentAt(database, sessionID, dbpkg.NormaliseAgentID(agentID), at)
				if qErr != nil {
					return qErr
				}
				if workItem == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "no work item held by %s at %s\n",
						dbpkg.NormaliseAgentID(agentID), claimledger.FormatTime(at))
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), workItem)
				return nil
			}

			episodes, qErr := dbpkg.ClaimEpisodesAt(database, at)
			if qErr != nil {
				return qErr
			}
			if len(episodes) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no claim episodes cover %s\n", claimledger.FormatTime(at))
				return nil
			}
			printClaimEpisodes(cmd, episodes)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "agent id (default \"__root__\" for the session owner)")
	cmd.Flags().StringVar(&sessionID, "session", "", "scope to one session (agent ids are shared across sessions)")
	cmd.Flags().StringVar(&atStr, "at", "", "RFC3339 instant to resolve (default now)")
	return cmd
}

func claimsListCmd() *cobra.Command {
	var workItemID, agentID string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded claim episodes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			database, err := openDB(dir)
			if err != nil {
				return err
			}
			defer database.Close()

			var episodes []dbpkg.ClaimEpisode
			switch {
			case workItemID != "":
				resolved, rErr := resolveID(dir, workItemID)
				if rErr != nil {
					resolved = workItemID
				}
				episodes, err = dbpkg.ClaimEpisodesForWorkItem(database, resolved)
			case agentID != "":
				episodes, err = dbpkg.ClaimEpisodesForAgent(database, dbpkg.NormaliseAgentID(agentID), limit)
			default:
				episodes, err = dbpkg.ListClaimEpisodes(database, limit)
			}
			if err != nil {
				return err
			}
			if len(episodes) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No claim episodes recorded.")
				return nil
			}
			printClaimEpisodes(cmd, episodes)
			return nil
		},
	}
	cmd.Flags().StringVar(&workItemID, "work-item", "", "show the ownership history of one work item")
	cmd.Flags().StringVar(&agentID, "agent", "", "show one agent's episodes")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum rows")
	return cmd
}

func printClaimEpisodes(cmd *cobra.Command, episodes []dbpkg.ClaimEpisode) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%-20s  %-18s  %-16s  %-20s  %-20s  %s\n",
		"WORK ITEM", "AGENT", "SESSION", "START", "END", "OUTCOME")
	for _, e := range episodes {
		end := "(open)"
		if !e.IsOpen() {
			end = e.EndedAt.Format(time.RFC3339)
		}
		outcome := e.Outcome
		if outcome == "" {
			outcome = "-"
		}
		fmt.Fprintf(out, "%-20s  %-18s  %-16s  %-20s  %-20s  %s\n",
			e.WorkItemID, truncate(e.AgentID, 18), truncate(e.SessionID, 16),
			e.StartedAt.Format(time.RFC3339), end, outcome)
	}
}

func claimsReconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Close episodes whose session died without releasing",
		Long: `An agent killed outright (SIGKILL, machine crash, container teardown) fires
neither the release path nor SessionEnd, so its episode keeps an empty end
forever. Readers treat an open episode as open-ended — correct while its session
lives. This pass supplies the missing end, with outcome "expired", for every
session that is no longer live by heartbeat.

It is idempotent and safe to run at any time.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			database, err := openDB(dir)
			if err != nil {
				return err
			}
			defer database.Close()

			store := claimLedgerStore(dir)
			res, rErr := store.Reconcile(claimLedgerLivePredicate(database, filepath.Dir(dir)), time.Time{})
			if rErr != nil {
				return rErr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reconcile: closed %d episode(s) across %d dead session(s)\n",
				res.Episodes, res.Sessions)
			return nil
		},
	}
}

func claimsArchiveCmd() *cobra.Command {
	var apply bool
	var olderThanDays int
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Compact closed sessions' claim shards into a single archive ledger",
		Long: `Roll the shards of closed, idle sessions into .wipnote/claims/archive.html,
bounding file growth while keeping the working set small. Archived episodes stay
fully queryable: the archive lives in the same directory and is read by the same
parser and the same reindex pass.

A shard is eligible only when ALL THREE hold: the session is not live by
heartbeat, every episode in it is closed, and nothing has happened in it since
the age threshold. Dry-run by default — pass --apply to execute.

Run 'wipnote claims reconcile' first if crashed sessions are leaving episodes
open; an open episode blocks its shard from ever becoming eligible.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			database, err := openDB(dir)
			if err != nil {
				return err
			}
			defer database.Close()

			cutoff := time.Now().UTC().Add(-time.Duration(olderThanDays) * 24 * time.Hour)
			store := claimLedgerStore(dir)
			res, aErr := store.Archive(cutoff, claimLedgerLivePredicate(database, filepath.Dir(dir)), apply)
			if aErr != nil {
				return aErr
			}

			out := cmd.OutOrStdout()
			verb := "would archive"
			if apply {
				verb = "archived"
			}
			for _, c := range res.Candidates {
				fmt.Fprintf(out, "  %-14s  %s  (%d episodes, idle since %s)\n",
					verb, truncate(c.RootSessionID, 20), len(c.Episodes),
					c.LastActivity.Format("2006-01-02"))
			}
			fmt.Fprintf(out, "Summary: %s %d session shard(s), %d episode(s) (threshold: idle >= %d days)\n",
				verb, len(res.Candidates), res.Episodes, olderThanDays)
			if !apply && len(res.Candidates) > 0 {
				fmt.Fprintln(out, "\nHint: run `wipnote claims archive --apply` to execute.")
			}
			if apply && len(res.Candidates) > 0 {
				fmt.Fprintln(out, "\nRun `wipnote reindex` to refresh the read index, or it will lazy-rebuild on next query.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the archive (default is dry-run)")
	cmd.Flags().IntVar(&olderThanDays, "older-than", defaultClaimArchiveAgeDays,
		"only archive shards idle for at least this many days")
	return cmd
}
