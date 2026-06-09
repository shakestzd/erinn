package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/core/slug"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// workitemCmd builds a standard CRUD command group for any work item type.
// Usage: workitemCmd("feature", "features"), workitemCmd("bug", "bugs"), etc.
func workitemCmd(typeName, dirName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   typeName,
		Short: "Manage " + dirName,
	}
	cmd.AddCommand(wiCreateCmd(typeName, dirName))
	cmd.AddCommand(wiListCmd(typeName, dirName))
	cmd.AddCommand(wiShowCmd(typeName))
	cmd.AddCommand(wiStartCmd(typeName))
	cmd.AddCommand(wiCompleteCmd(typeName))
	cmd.AddCommand(wiDeleteCmd(typeName))
	cmd.AddCommand(wiAddStepCmd(typeName))
	cmd.AddCommand(wiAddTaskStepCmd(typeName))
	cmd.AddCommand(wiCompleteTaskStepCmd(typeName))
	cmd.AddCommand(wiUpdateCmd(typeName))
	cmd.AddCommand(setDescriptionCmd(typeName))
	if typeName != "track" {
		cmd.AddCommand(wiMoveCmd(typeName))
	}
	return cmd
}

func wiListCmd(_ string, dirName string) *cobra.Command {
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
	dir, err := findWipnoteDir()
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

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})

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
	var format string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show " + typeName + " details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiShowWithFormat(args[0], format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: json or text")
	return cmd
}

func runWiShow(id string) error {
	return runWiShowWithFormat(id, "text")
}

// runWiShowWithFormat shows a work item in the requested format (text or json).
func runWiShowWithFormat(id, format string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	resolved, err := resolveID(dir, id)
	if err != nil {
		return err
	}
	path := resolveNodePath(dir, resolved)
	if path == "" {
		kind := kindFromPrefix(resolved)
		return workitem.ErrNotFoundOnDisk(kind, resolved)
	}
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	switch format {
	case "json":
		return printNodeDetailJSON(node)
	default:
		printNodeDetail(node)
		return nil
	}
}

// printNodeDetailJSON outputs a node as indented JSON.
func printNodeDetailJSON(node *models.Node) error {
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// wiForceStart is set by --force on the start sub-command and consumed by
// wiSetStatusWithAgent to bypass the live-collision refusal.
var wiForceStart bool

func wiStartCmd(typeName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <id>",
		Short: "Mark a " + typeName + " as in-progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiSetStatus(typeName, args[0], "in-progress")
		},
	}
	cmd.Flags().BoolVar(&wiForceStart, "force", false,
		"claim anyway even when a live collision is detected (emits a warning and proceeds)")
	return cmd
}

// wiAllowSpecSkip and wiAllowDirtyComplete are set by completion flags and
// consumed by the completion gates below. Package-level because
// wiSetStatusWithAgent has many test callers and we don't want to thread
// parameters through all of them just for opt-in overrides.
var wiAllowSpecSkip bool
var wiAllowDirtyComplete bool

// wiAcceptedAdvisory carries the --accepted-advisory reason. When non-empty
// it provides an audited override of the provenance completion gate: a
// code-bearing item may be completed with zero linked source commits, but
// the operator-supplied rationale is recorded on the .wipnote artifact and
// surfaced by `wipnote check accepted-advisory` and `wipnote show`.
var wiAcceptedAdvisory string

// acceptedAdvisoryMarker prefixes the content note the override rationale is
// persisted under. Node.Properties does NOT round-trip through the canonical
// HTML writer/parser (only edge properties do), but Node.Content does (via
// section[data-content]); so the audited reason is stored as a content note
// with this stable, parseable prefix and recovered with acceptedAdvisoryOf.
const acceptedAdvisoryMarker = "accepted-advisory (provenance override): "

// acceptedAdvisoryOf returns the recorded provenance-advisory reason for a
// node, or "" if none. The reason was written into the node content as a
// note prefixed with acceptedAdvisoryMarker.
func acceptedAdvisoryOf(n *models.Node) string {
	if n == nil || n.Content == "" {
		return ""
	}
	for _, line := range strings.Split(n.Content, "\n") {
		s := strings.TrimSpace(stripHTMLTags(line))
		if idx := strings.Index(s, acceptedAdvisoryMarker); idx >= 0 {
			return strings.TrimSpace(s[idx+len(acceptedAdvisoryMarker):])
		}
	}
	return ""
}

func wiCompleteCmd(typeName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <id>",
		Short: "Mark a " + typeName + " as done",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiSetStatus(typeName, args[0], "done")
		},
	}
	if typeName == "feature" {
		cmd.Flags().BoolVar(&wiAllowSpecSkip, "allow-spec-skip", false,
			"bypass spec_enforcement.feature_complete gate; intended for emergency overrides only")
	}
	if shouldAutocommitWorkitemArtifact(typeName) {
		cmd.Flags().BoolVar(&wiAllowDirtyComplete, "allow-dirty", false,
			"bypass the uncommitted source gate; intended for intentional dirty-tree completion only")
		cmd.Flags().StringVar(&wiAcceptedAdvisory, "accepted-advisory", "",
			"audited override of the zero-commit provenance gate; records the rationale on the artifact")
	}
	return cmd
}

func runWiSetStatus(typeName, id, status string) error {
	sessionID := hooks.EnvSessionID("")
	agentID := dbpkg.NormaliseAgentID(os.Getenv("WIPNOTE_AGENT_ID"))
	return wiSetStatusWithAgent(typeName, id, status, sessionID, agentID)
}

func writesLegacyActiveFeature(agentID string) bool {
	switch agentID {
	case dbpkg.AgentRootSentinel, "codex":
		return true
	default:
		return false
	}
}

// wiSetStatusWithAgent is the testable core of runWiSetStatus that accepts
// explicit sessionID and agentID instead of reading them from the environment.
// This allows concurrent tests to call it with distinct agent identities without
// env-var races.
func wiSetStatusWithAgent(typeName, id, status, sessionID, agentID string) error {
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

	// CRISPI spec-enforcement gate: when completing a feature with the
	// gate opted in via config, refuse if the feature HTML has no usable
	// spec section. --allow-spec-skip provides an audited bypass.
	if typeName == "feature" && status == "done" && !wiAllowSpecSkip {
		if err := checkFeatureCompleteSpecGate(dir, id); err != nil {
			return err
		}
	}

	if status == "done" && shouldAutocommitWorkitemArtifact(typeName) {
		if err := checkUncommittedSourceCompleteGate(dir, id, wiAllowDirtyComplete); err != nil {
			return err
		}
	}

	// Provenance completion gate (feat-7b593272). Type-agnostic: covers
	// code-bearing feature/bug/spike items. Runs BEFORE col.Complete and
	// slice-1's transactional commit path so a blocked item never reaches
	// the "done" transition — composing cleanly with the transactional
	// complete (no compensating re-open needed because no transition fired).
	// --accepted-advisory provides an audited override that records the
	// rationale on the .wipnote artifact for compliance/snapshot tooling.
	if status == "done" && shouldAutocommitWorkitemArtifact(typeName) {
		if err := checkProvenanceCompleteGate(p, col, typeName, id, wiAcceptedAdvisory); err != nil {
			return err
		}
	}
	if status == "done" && shouldAutocommitWorkitemArtifact(typeName) {
		if strings.TrimSpace(wiAcceptedAdvisory) != "" {
			// --accepted-advisory is an audited override that composes past the
			// gate-record guard in the same way it already overrides the
			// provenance gate above. The rationale is already being recorded on
			// the artifact by checkProvenanceCompleteGate; we simply skip guard 4
			// here so the operator is not double-blocked when the gate cannot
			// produce a passing record (e.g. manifest-less or broken runner).
			fmt.Fprintln(os.Stderr, "gate-record check bypassed via --accepted-advisory")
		} else if err := checkCompletionGateRecord(p.DB, filepath.Dir(dir), sessionID, id); err != nil {
			return err
		}
	}

	// Capture the artifact's pre-commit HEAD BEFORE col.Complete flushes the
	// canonical HTML to disk. The transactional complete path compares this
	// against the post-commit HEAD to assert the commit actually advanced.
	transactionalComplete := status == "done" && shouldAutocommitWorkitemArtifact(typeName)
	var artifactPreHead string
	if transactionalComplete {
		repoRoot := filepath.Dir(dir)
		absArtifact := filepath.Join(dir, typeName+"s", id+".html")
		artifactPreHead = artifactHeadCommit(repoRoot, absArtifact)
	}

	// Live-collision gate (feat-5a9839fb): before acquiring the claim, check
	// whether another live session already holds this item. A live collision
	// (foreign session with recent heartbeat) is a HARD REFUSAL unless --force
	// was supplied. A stale/dead claim is NOT a refusal — the item is reclaimable.
	if status == "in-progress" && p.DB != nil {
		livenessWindow := dbpkg.LivenessStalenessThreshold(filepath.Dir(dir))
		lc, lcErr := dbpkg.LiveCollision(p.DB, id, sessionID, livenessWindow)
		if lcErr == nil && lc.HasLiveCollision {
			if !wiForceStart {
				return fmt.Errorf("%s", dbpkg.LiveCollisionMessage(lc))
			}
			// --force: emit the CollaborationSummary warning, then proceed.
			if msg := dbpkg.CollaborationSummary(lc.CollaborationState); msg != "" {
				fmt.Fprintln(os.Stderr, msg)
			}
		}
	}

	var node *models.Node
	switch status {
	case "in-progress":
		node, err = col.Start(id)
	case "blocked":
		err = col.Edit(id).SetStatus("blocked").Save()
		if err == nil {
			node, err = col.Get(id)
		}
	default:
		node, err = col.Complete(id)
	}
	if err != nil {
		return fmt.Errorf("cannot set %s %s to %s: %w\nRun 'wipnote wip' to see active items or 'wipnote %s list' to see valid IDs.", typeName, id, status, err, typeName)
	}

	// When starting a work item, update per-agent attribution, create a claim
	// with per-agent attribution, and create an implemented_in edge.
	if status == "in-progress" {
		if sessionID != "" {
			if p.DB != nil {
				currentActive := dbpkg.GetActiveWorkItem(p.DB, sessionID, agentID)
				if currentActive != id {
					// New per-agent attribution table (primary write path).
					_ = dbpkg.SetActiveWorkItem(p.DB, sessionID, agentID, id)
					// Legacy dual-write to sessions.active_feature_id is single-row
					// shared state. When N parallel subagents each claim a different
					// work item in the same session, they race on this one column
					// and corrupt each other's attribution (bug-d2d3fb3f). Gate the
					// write to the root agent only: root stays authoritative for
					// consumers still reading the legacy column; subagents rely on
					// per-agent claims + active_work_items for their own attribution.
					if writesLegacyActiveFeature(agentID) {
						_ = hooks.UpdateActiveFeature(p.DB, sessionID, id)
					}
				}
				// Always write (or renew) the claim row regardless of whether
				// active_work_items already shows this item for (session, agent).
				// active_work_items and claims are separate tables that can diverge:
				// an expired or never-written claim row causes ClaimedItem=="" in
				// the PreToolUse guard, blocking all Write/Edit. ClaimItemOrRenew
				// is idempotent — it refreshes an existing live claim's lease or
				// inserts a new row if none exists (bug-0d55d8e4).
				claim := &models.Claim{
					ClaimID:          "clm-" + uuid.NewString()[:8],
					WorkItemID:       id,
					OwnerSessionID:   sessionID,
					OwnerAgent:       agentForClaim(),
					ClaimedByAgentID: agentID,
					Status:           models.ClaimInProgress,
				}
				_ = dbpkg.ClaimItemOrRenew(p.DB, claim, 30*time.Minute)
			}
			autoImplementedInEdge(col, id, sessionID, p.DB)
			// Non-fatal advisory: warn when this session now owns >= wipPerSessionSoftLimit
			// in-progress items. Never blocks; yolo/orchestrator may legitimately pre-start.
			if allItems, scanErr := scanInProgress(dir); scanErr == nil {
				bysess := wipGroupBySession(allItems)
				if len(bysess[sessionID]) >= wipPerSessionSoftLimit {
					fmt.Fprintf(os.Stderr,
						"wip advisory: session %s now owns %d in-progress items (soft limit %d) — consider completing before starting more\n",
						truncate(sessionID, 16), len(bysess[sessionID]), wipPerSessionSoftLimit)
				}
			}
		}
	}

	// When completing a work item, clear active_work_items and the legacy
	// active_feature_id on any session still pointing at it.
	if status == "done" && p.DB != nil {
		if sessionID != "" {
			_ = dbpkg.ClearActiveWorkItem(p.DB, sessionID, agentID)
		}
		// Clear legacy column for any session pointing at this item.
		_, _ = p.DB.Exec(
			`UPDATE sessions SET active_feature_id = '' WHERE active_feature_id = ?`,
			id,
		)
	}

	// Commit the artifact HTML to the main git repo on every state transition
	// so that YOLO/worktree-based runs (where .wipnote/ is suppressed by the
	// per-worktree exclude) never lose the state file at any point in the
	// lifecycle. The commit is non-fatal: if git is unavailable or the commit
	// fails for any reason (hook rejection, nothing to commit, non-git project),
	// we log to stderr and continue. State change does not depend on the commit.
	// Gate with an explicit allowlist via shouldAutocommitWorkitemArtifact:
	// plans have their own atomic commit path (commitPlanChange in
	// plan_yaml_cmds.go) that handles YAML+HTML together.
	if transactionalComplete {
		// Complete path is transactional: a failed artifact commit must NOT
		// leave the item silently "done" with no durable record. On failure
		// perform a compensating re-open (status → in-progress) so the item's
		// state matches reality, then exit non-zero with the exact remediation
		// command. The re-open's own side effects (lineage edges, session
		// events, attribution) are accepted and coherent with a re-open.
		if cerr := commitArtifactTransactional(dir, typeName, id, artifactPreHead); cerr != nil {
			// Environmental read-only filesystem: the artifact is on disk and the
			// item is logically done — do NOT reopen. Emit an advisory to stderr
			// and return nil so the item stays "done".
			if errors.Is(cerr, errReadOnlyFS) {
				relArtifact := filepath.Join(".wipnote", typeName+"s", id+".html")
				fmt.Fprintf(os.Stderr,
					"autocommit skipped: .git is read-only (sandboxed). Item marked done. "+
						"Commit manually: git add %s && git commit -m %q\n",
					relArtifact, "wipnote: complete "+id)
				return nil
			}
			// Compensating re-open: use col.Start, the codebase's canonical
			// revert transition. Unlike Edit().SetStatus(), Start dual-writes
			// status "in-progress" to SQLite so the HTML (canonical) and the
			// read index stay coherent — a re-open that updated only the HTML
			// would leave the DB falsely reporting "done".
			_, reopenErr := col.Start(id)
			if p.DB != nil {
				_, _ = p.DB.Exec(
					`UPDATE sessions SET active_feature_id = '' WHERE active_feature_id = ?`,
					id,
				)
			}
			WriteStatuslineCache(dir, id)
			remediation := fmt.Sprintf("wipnote %s complete %s", typeName, id)
			if reopenErr != nil {
				return fmt.Errorf(
					"completion aborted: artifact commit failed for %s (%v) and the compensating re-open ALSO failed (%v).\n"+
						"The item may be left in an inconsistent state — inspect with 'wipnote %s show %s', "+
						"manually restore the artifact, then rerun:\n  %s",
					id, cerr, reopenErr, typeName, id, remediation)
			}
			return fmt.Errorf(
				"completion aborted: artifact commit failed for %s: %v\n"+
					"The item has been re-opened (status: in-progress) so its state matches reality. "+
					"Resolve the commit blocker (e.g. unlock the git index, fix a rejecting hook, or commit manually), then rerun:\n  %s",
				id, cerr, remediation)
		}
	} else if shouldAutocommitWorkitemArtifact(typeName) {
		action := actionFromStatus(status)
		if err := commitWipnoteArtifact(dir, typeName, id, action); err != nil {
			fmt.Fprintf(os.Stderr, "autocommit warning: %v\n", err)
		}
	}

	// Update status line cache for subagent visibility.
	if status == "in-progress" {
		WriteStatuslineCache(dir, id)
	} else {
		WriteStatuslineCache(dir, "")
	}

	verb := "Started"
	switch status {
	case "done":
		verb = "Completed"
	case "blocked":
		verb = "Blocked"
	}
	fmt.Printf("%s: %s  %s\n", verb, node.ID, node.Title)
	if status == "done" && strings.TrimSpace(wiAcceptedAdvisory) != "" {
		fmt.Printf("  accepted-advisory: %s\n", strings.TrimSpace(wiAcceptedAdvisory))
	}

	// On start, print a session-label hint tailored to the active harness.
	if status == "in-progress" {
		printStartTip(typeName, node.Title)
	}

	return nil
}

func printStartTip(typeName, title string) {
	titleSlug := slug.Make(title, 30)
	color := slug.WorkItemColor(typeName)
	switch currentHarness() {
	case "claude":
		fmt.Printf("\nTip: sync your Claude session label to this item:\n")
		fmt.Printf("  /rename %s\n", titleSlug)
		fmt.Printf("  /color %s\n", color)
	default:
		fmt.Printf("\nTip: keep this session aligned with the item:\n")
		fmt.Printf("  label: %s\n", titleSlug)
		fmt.Printf("  color: %s\n", color)
	}
}

func currentHarness() string {
	for _, v := range []string{os.Getenv("WIPNOTE_AGENT_TYPE"), os.Getenv("WIPNOTE_AGENT_ID")} {
		switch {
		case strings.Contains(v, "codex"):
			return "codex"
		case strings.Contains(v, "claude"):
			return "claude"
		case strings.Contains(v, "gemini"):
			return "gemini"
		}
	}
	return "claude"
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
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiDelete(args[0])
		},
	}
}

func runWiDelete(id string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	resolved, err := resolveID(dir, id)
	if err != nil {
		return err
	}
	path := resolveNodePath(dir, resolved)
	if path == "" {
		kind := kindFromPrefix(resolved)
		return workitem.ErrNotFoundOnDisk(kind, resolved)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete %s: %w", resolved, err)
	}
	fmt.Printf("Deleted: %s\n", resolved)
	return nil
}

func wiAddStepCmd(typeName string) *cobra.Command {
	var allowHostPaths bool
	cmd := &cobra.Command{
		Use:   "add-step <id> <description>",
		Short: "Add an implementation step to a " + typeName,
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiAddStep(typeName, args[0], args[1], allowHostPaths)
		},
	}
	cmd.Flags().BoolVar(&allowHostPaths, "allow-host-paths", false, "bypass host-local path check in step description")
	return cmd
}

func runWiAddStep(typeName, id, description string, allowHostPaths bool) error {
	if err := validateDescriptionForHostPaths(description, allowHostPaths); err != nil {
		return err
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
	if err := col.Edit(id).AddStep(description).Save(); err != nil {
		return fmt.Errorf("add step: %w", err)
	}
	fmt.Printf("Added step to %s: %s\n", id, description)
	return nil
}

// wiAddTaskStepCmd registers `add-task-step` — a hook-only command that adds a
// step with StepID="task-<taskID>" so CompleteTaskStep can find and tick it later.
// Used by internal/hooks/task_tracking.go addTaskStep (TaskCreated hook).
func wiAddTaskStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:    "add-task-step <id> <task-id> <description>",
		Short:  "Add a task-associated step (hook-only)",
		Args:   cobra.ExactArgs(3),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiAddTaskStep(typeName, args[0], args[1], args[2])
		},
	}
}

func runWiAddTaskStep(typeName, id, taskID, description string) error {
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
	if err := col.AddTaskStep(id, taskID, description); err != nil {
		return fmt.Errorf("add task step: %w", err)
	}
	return nil
}

// wiCompleteTaskStepCmd registers `complete-task-step` — a hook-only command
// that flips data-completed=true on the step with StepID="task-<taskID>".
// Used by internal/hooks/task_tracking.go completeTaskStep (TaskCompleted hook).
func wiCompleteTaskStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:    "complete-task-step <id> <task-id>",
		Short:  "Mark a task-associated step as completed (hook-only)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiCompleteTaskStep(typeName, args[0], args[1])
		},
	}
}

func runWiCompleteTaskStep(typeName, id, taskID string) error {
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
	if err := col.CompleteTaskStep(id, taskID); err != nil {
		return fmt.Errorf("complete task step: %w", err)
	}
	return nil
}

// splitSteps splits a comma-separated steps string into trimmed non-empty parts.
func splitSteps(s string) []string {
	var steps []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			steps = append(steps, trimmed)
		}
	}
	return steps
}

// agentForClaim returns the agent string for claim ownership.
func agentForClaim() string {
	if v := os.Getenv("WIPNOTE_AGENT_TYPE"); v != "" {
		return v
	}
	return "claude-code"
}

// resolveID resolves a partial or full work item ID to its canonical form.
func resolveID(wipnoteDir, id string) (string, error) {
	return workitem.ResolvePartialID(wipnoteDir, id)
}

// resolveNodePath searches all subdirectories for a file matching id.
func resolveNodePath(wipnoteDir, id string) string {
	dirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	for _, sub := range dirs {
		p := filepath.Join(wipnoteDir, sub, id+".html")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func printNodeDetail(n *models.Node) {
	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  %s\n", n.Title)
	fmt.Println(sep)
	fmt.Printf("  ID        %s\n", n.ID)
	fmt.Printf("  Type      %s\n", n.Type)
	fmt.Printf("  Status    %s\n", n.Status)
	fmt.Printf("  Priority  %s\n", n.Priority)
	if n.TrackID != "" {
		fmt.Printf("  Track     %s\n", n.TrackID)
	}
	if !n.CreatedAt.IsZero() {
		fmt.Printf("  Created   %s\n", n.CreatedAt.Format("2006-01-02"))
	}

	// Provenance line — surface the harness/model/role/CLI version captured
	// at creation. Always print so consumers see an explicit "unknown" rather
	// than silently missing context for legacy items (feat-40ef1333).
	prov := provenance.Provenance{
		Agent:      n.CreatedByAgent,
		Model:      n.CreatedByModel,
		Role:       n.CreatedByRole,
		CLIVersion: n.CreatedByCLIVersion,
	}
	fmt.Printf("  Created by  %s\n", prov.HumanString())

	if adv := acceptedAdvisoryOf(n); adv != "" {
		fmt.Printf("  Accepted-advisory  %s\n", adv)
	}

	if len(n.Steps) > 0 {
		done := 0
		for _, s := range n.Steps {
			if s.Completed {
				done++
			}
		}
		fmt.Printf("\nSteps: %d/%d complete\n", done, len(n.Steps))
		for _, s := range n.Steps {
			tick := "[ ]"
			if s.Completed {
				tick = "[x]"
			}
			fmt.Printf("  %s  %s\n", tick, s.Description)
		}
	}

	if len(n.Edges) > 0 {
		fmt.Println("\nEdges:")
		for rel, edges := range n.Edges {
			for _, e := range edges {
				fmt.Printf("  %-15s → %s\n", rel, e.TargetID)
			}
		}
	}

	if n.Content != "" {
		fmt.Println("\nContent:")
		for _, line := range strings.Split(n.Content, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}

	// Hint for finalized plans: surface the idempotent dispatch command.
	if n.Type == "plan" && string(n.Status) == "finalized" {
		fmt.Printf("\nNext: wipnote plan finalize-yaml %s   (idempotent — creates features, embeds decisions, prints dispatch summary)\n", n.ID)
	}
}

// kindFromPrefix determines the work item kind from an ID prefix.
func kindFromPrefix(id string) string {
	if strings.HasPrefix(id, "feat-") {
		return "feature"
	}
	if strings.HasPrefix(id, "bug-") {
		return "bug"
	}
	if strings.HasPrefix(id, "spk-") {
		return "spike"
	}
	if strings.HasPrefix(id, "trk-") {
		return "track"
	}
	if strings.HasPrefix(id, "pln-") {
		return "plan"
	}
	if strings.HasPrefix(id, "spc-") {
		return "spec"
	}
	return "work item"
}

// checkProvenanceCompleteGate is the type-agnostic provenance gate for
// code-bearing bug/feature/spike completion. It runs as a pre-completion
// gate (before col.Complete and slice-1's transactional commit) so a
// blocked item is never transitioned to "done".
//
// Decision:
//   - Item not code-bearing (no non-.wipnote file recorded in
//     feature_files, or no DB) → exempt, complete normally.
//   - Code-bearing AND >=1 linked source commit
//     (db.GetCommitsByFeature) → complete normally.
//   - Code-bearing AND zero linked commits AND no advisory → BLOCK with a
//     non-zero error carrying the --accepted-advisory remediation.
//   - Code-bearing AND zero linked commits AND advisory set → complete,
//     persisting the rationale on the .wipnote artifact (Properties +
//     audit note) and emitting a stderr warning.
func checkProvenanceCompleteGate(p *workitem.Project, col *workitem.Collection, typeName, id, advisory string) error {
	if p == nil || p.DB == nil {
		// No read index available — cannot evaluate provenance. Do not
		// block: the canonical store stays the source of truth and the
		// uncommitted-source gate already covers dirty trees.
		return nil
	}

	codePaths, err := dbpkg.CodeBearingPaths(p.DB, id, filepath.Dir(p.ProjectDir))
	if err != nil {
		return fmt.Errorf("provenance gate: inspect code-bearing paths for %s: %w", id, err)
	}
	if len(codePaths) == 0 {
		// Pure-.wipnote/doc item — exempt.
		return nil
	}

	commits, err := dbpkg.GetCommitsByFeature(p.DB, id)
	if err != nil {
		return fmt.Errorf("provenance gate: inspect linked commits for %s: %w", id, err)
	}
	if len(commits) > 0 {
		// At least one linked source commit — provenance satisfied.
		return nil
	}

	advisory = strings.TrimSpace(advisory)
	if advisory == "" {
		preview := codePaths
		if len(preview) > 5 {
			preview = preview[:5]
		}
		return fmt.Errorf(
			"refusing to complete %s %s: it is code-bearing (touched %d source path(s) outside .wipnote/, e.g. %s) "+
				"but has zero linked source commits — no durable provenance for the implementation.\n"+
				"Commit the implementation and link it, then rerun:\n  wipnote %s complete %s\n"+
				"To intentionally accept completion without a source commit (records an audited rationale on the artifact), rerun with:\n"+
				"  wipnote %s complete %s --accepted-advisory \"<reason>\"",
			typeName, id, len(codePaths), strings.Join(preview, ", "),
			typeName, id, typeName, id)
	}

	// Audited override: persist the rationale on the canonical artifact so
	// `wipnote check accepted-advisory`, `wipnote show`, and snapshot tooling
	// surface it. Persist BEFORE col.Complete so the property survives the
	// completion flush and the transactional commit captures it.
	if err := col.Edit(id).
		AddNote(acceptedAdvisoryMarker + advisory).
		Save(); err != nil {
		return fmt.Errorf("provenance gate: record accepted-advisory on %s: %w", id, err)
	}
	fmt.Fprintf(os.Stderr,
		"accepted-advisory warning: completing code-bearing %s %s with zero linked source commits.\n  reason: %s\n",
		typeName, id, advisory)
	return nil
}

// checkFeatureCompleteSpecGate enforces config.spec_enforcement.feature_complete:
// the feature HTML's <section class="spec"> must exist and contain at least one
// usable criterion (either an OpenSpec ### Requirement: with a non-empty SHALL
// line, or a legacy [ ]/[x]/[F] checkbox line under ## Acceptance Criteria).
//
// Returns nil when the gate is disabled, the feature has a non-empty spec, or
// allowSpecSkip is set. Returns a remediation error otherwise.
func checkFeatureCompleteSpecGate(wipnoteDir, featureID string) error {
	enforcement := hooks.ReadSpecEnforcement(filepath.Dir(wipnoteDir))
	if !enforcement.FeatureComplete {
		return nil
	}

	featurePath := filepath.Join(wipnoteDir, "features", featureID+".html")
	raw, err := os.ReadFile(featurePath)
	if err != nil {
		// Feature file unreadable — let the normal Complete path raise the
		// canonical error; we do not block on missing files.
		return nil
	}
	specContent := extractSpecSection(string(raw))
	if specContent == "" {
		return fmt.Errorf("feature %s has no spec section; run `wipnote spec generate %s --insert` first (or invoke /wipnote:spec-from-slice on Claude). Override with --allow-spec-skip if intentional.",
			featureID, featureID)
	}
	criteria := parseCriteria(unwrapPreBlock(specContent))
	if len(criteria) == 0 {
		return fmt.Errorf("feature %s spec section has 0 criteria; populate Requirements or Acceptance Criteria, or override with --allow-spec-skip",
			featureID)
	}
	return nil
}

func checkCompletionGateRecord(database *sql.DB, projectRoot, sessionID, workItemID string) error {
	if database == nil {
		return nil
	}
	codePaths, err := dbpkg.CodeBearingPaths(database, workItemID, projectRoot)
	if err != nil {
		return fmt.Errorf("quality gate: inspect code-bearing paths for %s: %w", workItemID, err)
	}
	if len(codePaths) == 0 {
		return nil
	}
	reportGuardProfileDrift(database, projectRoot, sessionID, os.Stderr)
	return validateCompletionGateRecord(projectRoot, database, sessionID, workItemID)
}

// unwrapPreBlock strips a leading/trailing <pre>...</pre> wrapper plus HTML
// entity escapes that slice 1's `spec --insert` writer applies. Leaves
// non-wrapped content untouched.
func unwrapPreBlock(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "<pre>") && strings.HasSuffix(t, "</pre>") {
		t = strings.TrimSuffix(strings.TrimPrefix(t, "<pre>"), "</pre>")
	}
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
	return r.Replace(t)
}
