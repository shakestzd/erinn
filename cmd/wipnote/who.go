// Register in main.go: root.AddCommand(whoCmd())
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/spf13/cobra"
)

// whoCmd implements `wipnote who` — prints the current session identity:
// claim owner, session family, harness, work item, execution root.
// Also surfaces any claim collision/collaboration state for the active item,
// and the files this session has touched (from feature_files.session_id).
func whoCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "who",
		Short: "Show current session identity and claim attribution",
		Long: `Show the identity, harness, session family, active work item claim,
and any concurrent-claimant (collision/collaboration) state for this session.
Also lists files this session has touched (current project only).

Fields exported:
  session_id        — this session's ID
  session_family_id — the launch family (shared across --resume continuations)
  harness           — detected CLI harness (claude-code, codex-cli, gemini-cli)
  work_item         — the active claimed work item (if any)
  claim_id          — the claim record ID
  claim_status      — claim lifecycle status
  execution_root    — root session for subagent chains
  collision         — warns when two+ sessions hold concurrent claims
  files             — files this session has touched (path, operation, last_seen)

Step/task event support per harness:
  claude-code  — TaskCreated/TaskCompleted mapped to task_created/task_completed
  codex-cli    — UNSUPPORTED: no native task lifecycle hooks; step tracking unavailable
  gemini-cli   — UNSUPPORTED: no native task lifecycle hooks; step tracking unavailable`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runWho(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON output")
	return cmd
}

// whoOutput is the stable JSON schema for `wipnote who --json`.
type whoOutput struct {
	SessionID       string           `json:"session_id"`
	SessionFamilyID string           `json:"session_family_id"`
	Harness         string           `json:"harness"`
	WorkItem        string           `json:"work_item,omitempty"`
	ClaimID         string           `json:"claim_id,omitempty"`
	ClaimStatus     string           `json:"claim_status,omitempty"`
	ClaimedAt       string           `json:"claimed_at,omitempty"`
	ExecutionRoot   string           `json:"execution_root,omitempty"`
	IsSubagent      bool             `json:"is_subagent"`
	// Live is derived from claim-heartbeat recency (NOT sessions.status). It is
	// the honest cross-harness liveness signal — true only when this session's
	// newest claim heartbeat is within the staleness threshold. A stale
	// status='active' ghost row reports Live=false (folds bug-6c3e8252).
	Live            bool             `json:"live"`
	Collaboration   *collabOutput    `json:"collaboration,omitempty"`
	TaskTracking    taskTrackingInfo `json:"task_tracking"`
	Files           []whoFileEntry   `json:"files"`
}

// whoFileEntry is a single file touched by this session.
//
// OverlapSessions lists OTHER live sessions that touched the same file within
// the recency window (Tier 1 live file-overlap detection). Non-empty means a
// potential concurrent-edit collision — rendered with a ⚠ marker in text mode.
type whoFileEntry struct {
	FilePath        string   `json:"file_path"`
	Operation       string   `json:"operation"`
	LastSeen        string   `json:"last_seen"`
	OverlapSessions []string `json:"overlap_sessions,omitempty"`
}

type collabOutput struct {
	HasCollision bool             `json:"has_collision"`
	Claimants    []claimantRecord `json:"claimants"`
}

type claimantRecord struct {
	ClaimID   string `json:"claim_id"`
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	ClaimedAt string `json:"claimed_at"`
}

// taskTrackingInfo describes per-harness task/step lifecycle support.
type taskTrackingInfo struct {
	Supported bool   `json:"supported"`
	Detail    string `json:"detail"`
}

func runWho(jsonOut bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	database, err := openDB(dir)
	if err != nil {
		return err
	}
	defer database.Close()

	// Resolve session ID: prefer env var, fall back to most-recent active.
	sessionID := hooks.EnvSessionID("")
	if sessionID == "" {
		sessionID, _ = dbpkg.MostRecentActiveSession(database)
	}

	// Detect the raw harness token from the environment. The launcher sets
	// WIPNOTE_AGENT_ID to the AgentID ("codex"/"gemini"), and subagents set it
	// to an arbitrary agent role name — neither is the display harness name
	// resolveTaskTrackingInfo keys on. Authoritative resolution happens below
	// once the claim identity (DB owner_agent) is loaded; this is the fallback.
	prov := provenance.Detect()
	rawHarness := prov.Agent

	// Resolve session family from DB.
	familyID := sessionID
	if sessionID != "" {
		var fid string
		if err := database.QueryRow(
			`SELECT COALESCE(session_family_id, session_id) FROM sessions WHERE session_id = ?`, sessionID,
		).Scan(&fid); err == nil && fid != "" {
			familyID = fid
		}
	}

	// Load claim identity.
	var identity *dbpkg.ClaimIdentity
	if sessionID != "" {
		identity, _ = dbpkg.GetClaimIdentity(database, sessionID)
	}

	// Resolve the display harness name. Prefer the claim/session DB owner_agent
	// (authoritative for what actually claimed the work, and correct even for
	// subagent sessions whose WIPNOTE_AGENT_ID is a role name, not a harness),
	// falling back to the env token. Normalize through the harness registry so
	// launcher AgentIDs ("codex"/"gemini") and DB IDs ("gemini_cli") map to the
	// canonical display name ("codex-cli"/"gemini-cli") before capability lookup.
	harnessToken := rawHarness
	if identity != nil && identity.Harness != "" {
		harnessToken = identity.Harness
	}
	displayHarness := harness.NormalizeDisplayName(harnessToken)
	if displayHarness == "" {
		displayHarness = "claude-code" // default when nothing is known
	}

	// Per-harness task tracking support.
	taskInfo := resolveTaskTrackingInfo(displayHarness)

	// Honest liveness: derive from claim-heartbeat recency, NOT sessions.status.
	// projectDir = parent of the .wipnote dir; used only to read the optional
	// liveness_staleness_seconds tunable from .wipnote/config.json.
	projectDir := filepath.Dir(dir)
	live := false
	if sessionID != "" {
		live = dbpkg.SessionLivenessByHeartbeat(
			database, sessionID, dbpkg.LivenessStalenessThreshold(projectDir))
	}

	// Files touched by this session (current project only, read-only). Two
	// ledgers are merged: feature_files (claimed touches) and session_files
	// (claimless touches — work done with no active feature, previously
	// invisible). Deduped on file_path, newest last_seen wins.
	fileSeen := map[string]int{} // file_path -> index into fileEntries
	var fileEntries []whoFileEntry
	addFile := func(sf dbpkg.SessionFile) {
		if idx, ok := fileSeen[sf.FilePath]; ok {
			if sf.LastSeen > fileEntries[idx].LastSeen {
				fileEntries[idx].Operation = sf.Operation
				fileEntries[idx].LastSeen = sf.LastSeen
			}
			return
		}
		fileSeen[sf.FilePath] = len(fileEntries)
		fileEntries = append(fileEntries, whoFileEntry{
			FilePath:  sf.FilePath,
			Operation: sf.Operation,
			LastSeen:  sf.LastSeen,
		})
	}
	if sessionID != "" {
		if sfiles, ferr := dbpkg.ListFilesBySession(database, sessionID); ferr == nil {
			for _, sf := range sfiles {
				addFile(sf)
			}
		}
		if cfiles, cerr := dbpkg.ListClaimlessFilesBySession(database, sessionID); cerr == nil {
			for _, sf := range cfiles {
				addFile(sf)
			}
		}
	}
	if fileEntries == nil {
		fileEntries = []whoFileEntry{}
	}

	// Live file-overlap detection (Tier 1): for each file this session has
	// touched, surface OTHER *live* sessions that touched the same path within
	// the recency window. Liveness is heartbeat-recency derived (Tier 3), so a
	// stale status='active' ghost session never produces a false ⚠
	// (bug-6c3e8252). Each lookup is a single indexed SELECT with zero writes.
	if sessionID != "" {
		window := dbpkg.FileOverlapWindow(projectDir)
		liveness := dbpkg.LivenessStalenessThreshold(projectDir)
		for i := range fileEntries {
			overlaps, oerr := dbpkg.FindLiveFileOverlaps(
				database, fileEntries[i].FilePath, sessionID, window, liveness)
			if oerr != nil {
				continue
			}
			for _, o := range overlaps {
				fileEntries[i].OverlapSessions = append(
					fileEntries[i].OverlapSessions, o.SessionID)
			}
		}
	}

	out := whoOutput{
		SessionID:       sessionID,
		SessionFamilyID: familyID,
		Harness:         displayHarness,
		IsSubagent:      false,
		Live:            live,
		TaskTracking:    taskInfo,
		Files:           fileEntries,
	}

	if identity != nil {
		out.WorkItem = identity.WorkItemID
		out.ClaimID = identity.ClaimID
		out.ClaimStatus = string(identity.ClaimStatus)
		out.ClaimedAt = identity.LeasedAt.UTC().Format(time.RFC3339)
		out.ExecutionRoot = identity.ExecutionRoot
		out.IsSubagent = identity.IsSubagent
		if identity.SessionFamilyID != "" {
			out.SessionFamilyID = identity.SessionFamilyID
		}

		// Check for collaboration/collision on the active work item.
		coll, err := dbpkg.DetectCollaboration(database, identity.WorkItemID)
		if err == nil && coll.HasCollision {
			collOut := &collabOutput{HasCollision: true}
			for _, c := range coll.Claimants {
				collOut.Claimants = append(collOut.Claimants, claimantRecord{
					ClaimID:   c.ClaimID,
					SessionID: c.OwnerSessionID,
					Harness:   c.OwnerAgent,
					ClaimedAt: c.LeasedAt.UTC().Format(time.RFC3339),
				})
			}
			out.Collaboration = collOut
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	return renderWhoText(out)
}

// resolveTaskTrackingInfo returns per-harness task lifecycle tracking support.
func resolveTaskTrackingInfo(displayHarness string) taskTrackingInfo {
	switch displayHarness {
	case "claude-code":
		return taskTrackingInfo{
			Supported: true,
			Detail:    "TaskCreated/TaskCompleted mapped to task_created/task_completed events",
		}
	case "codex-cli":
		return taskTrackingInfo{
			Supported: false,
			Detail:    "UNSUPPORTED: codex-cli has no native task lifecycle hooks; step tracking unavailable",
		}
	case "gemini-cli":
		return taskTrackingInfo{
			Supported: false,
			Detail:    "UNSUPPORTED: gemini-cli has no native task lifecycle hooks; step tracking unavailable",
		}
	default:
		return taskTrackingInfo{
			Supported: false,
			Detail:    fmt.Sprintf("unknown harness %q; task lifecycle tracking status unknown", displayHarness),
		}
	}
}

func renderWhoText(out whoOutput) error {
	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Session Identity\n")
	fmt.Println(sep)
	fmt.Printf("  Session ID:      %s\n", out.SessionID)
	fmt.Printf("  Family ID:       %s\n", out.SessionFamilyID)
	fmt.Printf("  Harness:         %s\n", out.Harness)
	if out.IsSubagent {
		fmt.Printf("  Role:            subagent\n")
		fmt.Printf("  Execution root:  %s\n", out.ExecutionRoot)
	} else {
		fmt.Printf("  Role:            root CLI\n")
	}
	liveLabel := "no (no recent claim heartbeat)"
	if out.Live {
		liveLabel = "yes (claim heartbeat recent)"
	}
	fmt.Printf("  Live:            %s\n", liveLabel)

	if out.WorkItem != "" {
		fmt.Printf("\n  Active claim:\n")
		fmt.Printf("    Work item:   %s\n", out.WorkItem)
		fmt.Printf("    Claim ID:    %s\n", out.ClaimID)
		fmt.Printf("    Status:      %s\n", out.ClaimStatus)
		fmt.Printf("    Claimed at:  %s\n", out.ClaimedAt)
	} else {
		fmt.Printf("\n  No active claim.\n")
	}

	if out.Collaboration != nil && out.Collaboration.HasCollision {
		fmt.Printf("\n  COLLISION/COLLABORATION DETECTED:\n")
		for _, c := range out.Collaboration.Claimants {
			fmt.Printf("    %s  session=%s  harness=%s  claimed=%s\n",
				c.ClaimID, c.SessionID, c.Harness, c.ClaimedAt)
		}
		fmt.Printf("  (warn-and-allow: work continues; coordinate manually)\n")
	}

	fmt.Printf("\n  Task tracking:   %s\n", out.TaskTracking.Detail)

	if len(out.Files) > 0 {
		fmt.Printf("\n  Files touched (%d):\n", len(out.Files))
		fmt.Printf("    %-3s %-10s  %-24s  %s\n", "", "OPERATION", "LAST SEEN", "PATH")
		fmt.Printf("    %s\n", strings.Repeat("-", 74))
		for _, f := range out.Files {
			marker := "  "
			if len(f.OverlapSessions) > 0 {
				marker = "⚠ "
			}
			fmt.Printf("    %-3s %-10s  %-24s  %s\n", marker, f.Operation, f.LastSeen, f.FilePath)
			if len(f.OverlapSessions) > 0 {
				fmt.Printf("        ⚠ also touched by live session(s): %s\n",
					strings.Join(f.OverlapSessions, ", "))
			}
		}
	} else {
		fmt.Printf("\n  No files recorded for this session.\n")
	}

	fmt.Println(sep)
	return nil
}
