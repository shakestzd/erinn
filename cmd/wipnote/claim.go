// Register in main.go: rootCmd.AddCommand(claimCmd())
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/hooks"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func claimCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Manage work item claims",
		Long: `Manage claims for coordinating multi-session work.

Claims represent exclusive access to a work item within a session, with automatic
expiry-based release and heartbeat-based renewal for live claims.`,
	}
	cmd.AddCommand(claimListCmd())
	cmd.AddCommand(claimShowCmd())
	cmd.AddCommand(claimReleaseCmd())
	cmd.AddCommand(claimHeartbeatCmd())
	return cmd
}

// claimListCmd lists claims from the SQLite DB.
func claimListCmd() *cobra.Command {
	var sessionID string
	var status string
	var limit int

	cmd := &cobra.Command{
		Use:   "list [--session <id>] [--status <status>] [--limit N]",
		Short: "List claims",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runClaimList(sessionID, status, limit)
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "Filter by session ID")
	cmd.Flags().StringVar(&status, "status", "", "Filter by claim status (proposed, claimed, in_progress, blocked, handoff_pending, completed, abandoned, expired)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of claims to show")
	return cmd
}

func runClaimList(sessionID, status string, limit int) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	claims, err := dbpkg.ListClaims(db, sessionID, status, limit)
	if err != nil {
		return fmt.Errorf("list claims: %w", err)
	}

	if len(claims) == 0 {
		fmt.Println("No claims found.")
		return nil
	}

	fmt.Printf("%-12s  %-10s  %-12s  %-8s  %-12s  %-5s  %s\n",
		"CLAIM", "ITEM", "OWNER", "STATUS", "AGENT", "FILES", "EXPIRES")
	fmt.Println(strings.Repeat("-", 97))

	for _, c := range claims {
		printClaimRow(&c)
	}

	fmt.Printf("\n%d claim(s)\n", len(claims))
	return nil
}

func printClaimRow(c *models.Claim) {
	claimID := truncate(c.ClaimID, 10)
	itemID := truncate(c.WorkItemID, 10)
	sessionID := truncate(c.OwnerSessionID, 12)
	agent := truncate(c.OwnerAgent, 12)
	expiry := c.LeaseExpiresAt.Local().Format("15:04:05")

	fileCount := 0
	if len(c.WriteScope) > 0 {
		var scope models.WriteScope
		if err := json.Unmarshal(c.WriteScope, &scope); err == nil {
			fileCount = len(scope.Paths)
		}
	}

	fmt.Printf("%-12s  %-10s  %-12s  %-8s  %-12s  %-5d  %s\n",
		claimID, itemID, sessionID, c.Status, agent, fileCount, expiry)
}

// claimShowCmd displays details about a specific claim.
func claimShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <claim-id>",
		Short: "Show claim details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runClaimShow(args[0])
		},
	}
}

func runClaimShow(claimID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	claim, err := dbpkg.GetClaim(db, claimID)
	if err != nil {
		return fmt.Errorf("get claim: %w", err)
	}
	if claim == nil {
		return fmt.Errorf("claim %q not found — claims expire after 30 minutes of inactivity\nRun 'wipnote claim list' to see active claims.", claimID)
	}

	fmt.Printf("Claim:    %s\n", claim.ClaimID)
	fmt.Printf("Item:     %s\n", claim.WorkItemID)
	fmt.Printf("Owner:    %s (%s)\n", claim.OwnerSessionID, claim.OwnerAgent)
	fmt.Printf("Status:   %s\n", claim.Status)
	fmt.Printf("Leased:   %s\n", claim.LeasedAt.Local().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Printf("Expires:  %s\n", claim.LeaseExpiresAt.Local().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Printf("Heartbeat: %s\n", claim.LastHeartbeatAt.Local().Format("2006-01-02T15:04:05Z07:00"))

	// Print write scope if non-empty
	if len(claim.WriteScope) > 0 {
		var scope models.WriteScope
		if err := json.Unmarshal(claim.WriteScope, &scope); err == nil && len(scope.Paths) > 0 {
			const maxDisplay = 20
			fmt.Printf("Scope:    %d file(s)\n", len(scope.Paths))
			shown := scope.Paths
			if len(shown) > maxDisplay {
				shown = shown[len(shown)-maxDisplay:]
			}
			for _, p := range shown {
				fmt.Printf("          %s\n", p)
			}
			if len(scope.Paths) > maxDisplay {
				fmt.Printf("          ... (%d total)\n", len(scope.Paths))
			}
		}
	}

	// Print optional fields
	if claim.TrackID != "" {
		fmt.Printf("Track:    %s\n", claim.TrackID)
	}
	if claim.IntendedOutput != "" {
		fmt.Printf("Output:   %s\n", claim.IntendedOutput)
	}
	if claim.ProgressNotes != "" {
		fmt.Printf("Notes:    %s\n", claim.ProgressNotes)
	}
	if claim.BlockerReason != "" {
		fmt.Printf("Blocker:  %s\n", claim.BlockerReason)
	}

	return nil
}

// claimReleaseCmd releases a claim by moving it to abandoned state.
func claimReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "release <claim-id>",
		Short: "Release a claim",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runClaimRelease(args[0])
		},
	}
}

func runClaimRelease(claimID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	// Get the claim to verify ownership and get session ID
	claim, err := dbpkg.GetClaim(db, claimID)
	if err != nil {
		return fmt.Errorf("get claim: %w", err)
	}
	if claim == nil {
		return fmt.Errorf("claim %q not found — claims expire after 30 minutes of inactivity\nRun 'wipnote claim list' to see active claims.", claimID)
	}

	// Release the claim (moves to abandoned state)
	if err := dbpkg.ReleaseClaim(db, claimID, claim.OwnerSessionID, models.ClaimAbandoned); err != nil {
		return fmt.Errorf("release claim: %w", err)
	}

	fmt.Printf("Released: %s (abandoned)\n", claimID)
	return nil
}

// claimHeartbeatCmd sends heartbeats to renew claim leases.
func claimHeartbeatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "heartbeat [<claim-id>]",
		Short: "Heartbeat claim(s) to renew lease",
		Long: `Send a heartbeat to renew a claim lease.

If a claim ID is provided, heartbeat that specific claim.
If no claim ID is provided, heartbeat all active claims for the current session.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			claimID := ""
			if len(args) > 0 {
				claimID = args[0]
			}
			return runClaimHeartbeat(claimID)
		},
	}
}

func runClaimHeartbeat(claimID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	db, err := openDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	// If a specific claim is given, heartbeat just that one
	if claimID != "" {
		return runHeartbeatOne(db, claimID)
	}

	// Otherwise heartbeat all active claims for the current session
	return runHeartbeatAllForSession(db)
}

func runHeartbeatOne(db *sql.DB, claimID string) error {
	claim, err := dbpkg.GetClaim(db, claimID)
	if err != nil {
		return fmt.Errorf("get claim: %w", err)
	}
	if claim == nil {
		return fmt.Errorf("claim %q not found — claims expire after 30 minutes of inactivity\nRun 'wipnote claim list' to see active claims.", claimID)
	}

	// Default lease duration (30 minutes)
	leaseDuration := 30 * time.Minute

	if err := dbpkg.HeartbeatClaim(db, claimID, claim.OwnerSessionID, leaseDuration); err != nil {
		return fmt.Errorf("heartbeat claim: %w", err)
	}

	fmt.Printf("Heartbeat: %s (expires %s)\n",
		claimID,
		time.Now().Add(leaseDuration).Local().Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

func runHeartbeatAllForSession(db *sql.DB) error {
	// Get current session ID from environment
	sessionID := hooks.EnvSessionID("")
	if sessionID == "" {
		// If not in a hook context, try to get the most recent active session
		var err error
		sessionID, err = dbpkg.MostRecentActiveSession(db)
		if err != nil || sessionID == "" {
			return fmt.Errorf("no active session found — cannot auto-detect claim\nSpecify the claim ID directly: 'wipnote claim heartbeat clm-xxxxxxxx'. Run 'wipnote claim list' to find it.")
		}
	}

	// List active claims for this session
	claims, err := dbpkg.ListActiveClaimsBySession(db, sessionID)
	if err != nil {
		return fmt.Errorf("list claims: %w", err)
	}

	if len(claims) == 0 {
		fmt.Printf("No active claims for session %s\n", sessionID)
		return nil
	}

	// Default lease duration (30 minutes)
	leaseDuration := 30 * time.Minute

	// Heartbeat each one
	for i := range claims {
		c := &claims[i]
		if err := dbpkg.HeartbeatClaim(db, c.ClaimID, c.OwnerSessionID, leaseDuration); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to heartbeat %s: %v\n", c.ClaimID, err)
			continue
		}

		fmt.Printf("Heartbeat: %s (expires %s)\n",
			c.ClaimID,
			time.Now().Add(leaseDuration).Local().Format("2006-01-02T15:04:05Z07:00"))
	}

	return nil
}
