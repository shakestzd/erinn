package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/core/slug"
	"github.com/shakestzd/wipnote/core/workitem"
	iarch "github.com/shakestzd/wipnote/internal/arch"
	commandspkg "github.com/shakestzd/wipnote/internal/commands"
	"github.com/spf13/cobra"
)

// workitemCmd builds a standard CRUD command group for any work item type.
// Usage: workitemCmd("feature", "features"), workitemCmd("bug", "bugs"), etc.
func workitemCmd(typeName, dirName string) *cobra.Command {
	return commandspkg.BuildWorkItemCommand(commandspkg.WorkItemOptions{
		TypeName:         typeName,
		DirName:          dirName,
		Create:           func() *cobra.Command { return wiCreateCmd(typeName, dirName) },
		List:             func() *cobra.Command { return wiListCmd(typeName, dirName) },
		Show:             func() *cobra.Command { return wiShowCmd(typeName) },
		Start:            func() *cobra.Command { return wiStartCmd(typeName) },
		Complete:         func() *cobra.Command { return wiCompleteCmd(typeName) },
		Delete:           func() *cobra.Command { return wiDeleteCmd(typeName) },
		AddStep:          func() *cobra.Command { return wiAddStepCmd(typeName) },
		AddTaskStep:      func() *cobra.Command { return wiAddTaskStepCmd(typeName) },
		CompleteTaskStep: func() *cobra.Command { return wiCompleteTaskStepCmd(typeName) },
		CompleteStep:     func() *cobra.Command { return wiCompleteStepCmd(typeName) },
		Update:           func() *cobra.Command { return wiUpdateCmd(typeName) },
		SetDescription:   func() *cobra.Command { return setDescriptionCmd(typeName) },
		Move:             func() *cobra.Command { return wiMoveCmd(typeName) },
	})
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
	// resolveNodeByUnionID resolves against both live and archived IDs together,
	// ensuring that ambiguous prefixes spanning both sources return an error.
	node, err := resolveNodeByUnionID(dir, id)
	if err != nil {
		return err
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

// wiLearning carries the --learning body text. When non-empty, completion
// validates the text as an arch card body and, on success, creates a card
// linked to the work item. A validation failure ABORTS the entire completion
// before any state change.
var wiLearning string

// wiResearchURL and wiResearchWaiver carry the --research-url / --research-waiver
// completion flags. When a code-bearing item changes a dependency manifest, the
// completion must cite at least one http(s) research URL OR record an explicit
// waiver — mirroring the v4 plan research gate (plan/planyaml validate). The
// cited evidence/waiver is persisted on the .wipnote artifact for audit
// (feat-d1bcbf10).
var wiResearchURL []string
var wiResearchWaiver string

// wiLearningKind is the --learning-kind flag value. Defaults to "decision"
// when empty. Must be one of: subsystem-map, invariant, hazard, decision.
var wiLearningKind string

// acceptedAdvisoryMarker prefixes the content note the override rationale is
// persisted under. This predates bug-c65a5f4e, when Node.Properties genuinely
// did not round-trip through the canonical HTML writer/parser and Node.Content
// (via section[data-content]) was the only durable place to put it. Properties
// round-trip now — feat-7ee73444's rollups rely on exactly that — but the
// content-note encoding is kept because existing artifacts carry it and
// acceptedAdvisoryOf reads them back by this stable, parseable prefix.
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
		cmd.Flags().StringArrayVar(&wiResearchURL, "research-url", nil,
			"http(s) URL of the docs/changelog verifying a dependency change; required (or --research-waiver) when the item changes a dependency manifest. Repeatable.")
		cmd.Flags().StringVar(&wiResearchWaiver, "research-waiver", "",
			"explicit waiver of the dependency-research completion gate; records the rationale on the artifact")
	}
	cmd.Flags().StringVar(&wiLearning, "learning", "",
		"arch card body text to capture as a durable learning; validation failure warns and skips attaching (non-fatal; completion still succeeds)")
	cmd.Flags().StringVar(&wiLearningKind, "learning-kind", "",
		"arch card kind for --learning (default: decision); one of: subsystem-map, invariant, hazard, decision")
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

	// Dependency-research completion gate (feat-d1bcbf10). When the item's
	// code-bearing paths include a dependency manifest (go.mod/package.json/…),
	// completion must cite a research URL or record an explicit waiver — mirroring
	// the v4 plan research gate. Runs after the provenance gate so it only fires
	// for items that actually carry committed source. --accepted-advisory does
	// NOT bypass it: research evidence and commit provenance are orthogonal.
	if status == "done" && shouldAutocommitWorkitemArtifact(typeName) {
		if err := checkDependencyResearchCompleteGate(p, col, typeName, id, wiResearchURL, wiResearchWaiver); err != nil {
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
		} else if err := checkCompletionGateRecord(nil, filepath.Dir(dir), sessionID, id); err != nil {
			return err
		}
	}

	// Capture the artifact's pre-commit HEAD BEFORE col.Complete flushes the
	// canonical HTML to disk. The transactional complete path compares this
	// against the post-commit HEAD to assert the commit actually advanced.
	transactionalComplete := status == "done" &&
		shouldAutocommitWorkitemArtifact(typeName) &&
		workitemArtifactCommitPolicyForEnv() == workitemArtifactCommitPolicySeparate
	deferredComplete := status == "done" &&
		shouldAutocommitWorkitemArtifact(typeName) &&
		workitemArtifactCommitPolicyForEnv() == workitemArtifactCommitPolicyDefer
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
			// Durable claim history (feat-21d12cdb). This sits BESIDE the claim
			// row, not inside it: claims/active_work_items are single-slot current
			// state that forget the moment a claim moves, so a signal emitted at
			// time T has nothing to join against. Open is idempotent — a re-start
			// or lease renewal writes no row.
			recordClaimEpisodeOpen(nil, dir, sessionID, agentID, id)
			autoImplementedInEdge(col, id, sessionID)
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
	if status == "done" {
		if sessionID != "" {
			// Close the claim episode in place, giving the interval its end.
			recordClaimEpisodeClose(nil, dir, sessionID, agentID, id, claimledger.OutcomeCompleted)
		}
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
			// revert transition.
			_, reopenErr := col.Start(id)
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
	} else if deferredComplete {
		if err := persistWorkitemArtifactTransition(dir, typeName, id, "complete"); err != nil {
			_, reopenErr := col.Start(id)
			WriteStatuslineCache(dir, id)
			remediation := fmt.Sprintf("wipnote %s complete %s", typeName, id)
			if reopenErr != nil {
				return fmt.Errorf(
					"completion aborted: failed to queue deferred artifact commit for %s (%v) and the compensating re-open ALSO failed (%v).\n"+
						"The item may be left in an inconsistent state — inspect with 'wipnote %s show %s', then rerun:\n  %s",
					id, err, reopenErr, typeName, id, remediation)
			}
			return fmt.Errorf(
				"completion aborted: failed to queue deferred artifact commit for %s: %v\n"+
					"The item has been re-opened (status: in-progress). Resolve the queue/outbox problem, then rerun:\n  %s",
				id, err, remediation)
		}
		fmt.Fprintf(os.Stderr,
			"artifact commit deferred by WIPNOTE_ARTIFACT_COMMIT_POLICY=defer for %s.\n  pending intent recorded; run: wipnote commit-queue flush\n",
			id)
	} else if shouldAutocommitWorkitemArtifact(typeName) {
		action := actionFromStatus(status)
		if err := persistWorkitemArtifactTransition(dir, typeName, id, action); err != nil {
			fmt.Fprintf(os.Stderr, "autocommit warning: %v\n", err)
		}
	}

	// Update status line cache for subagent visibility.
	if status == "in-progress" {
		WriteStatuslineCache(dir, id)
	} else {
		WriteStatuslineCache(dir, "")
	}

	// Create the learning arch card after successful completion.
	// Learning validation is NON-FATAL: the work item is already done (state changed),
	// so an invalid learning must not block completion. Instead, warn and skip attaching.
	if status == "done" && strings.TrimSpace(wiLearning) != "" {
		// Validate the learning body and kind (non-fatally, after state change).
		learningBody := strings.TrimSpace(wiLearning)
		validationErr := validateLearningKind(wiLearningKind)
		if validationErr == nil {
			validationErr = validateLearningBody(learningBody)
		}

		if validationErr == nil {
			// Validation passed: create the learning card.
			touchedPaths, _ := resolveWorkItemPaths(id, dir)
			if cerr := createLearningCard(dir, id, learningBody, wiLearningKind, touchedPaths); cerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to create learning card: %v\n", cerr)
			}
		} else {
			// Validation failed: emit warning and print remediation command.
			// The correct way to attach a learning post-completion is via
			// "wipnote arch add", not a non-existent "<type> edit --learning".
			// Use a known-valid default kind (decision) in the remediation command.
			// --links accepts work item IDs; --paths accepts file glob patterns.
			slug := "learning-" + id
			fmt.Fprintf(os.Stderr,
				"warning: --learning validation failed (learning NOT attached): %v\n"+
					"learning body was: %q\n"+
					"to attach a valid learning, run:\n  wipnote arch add %s --kind decision --body %s --links %s --created-by wipnote-completion\n",
				validationErr,
				learningBody,
				shellQuote(slug),
				shellQuote(learningBody),
				shellQuote(id))
		}
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

	// Post-completion drift nudge: emit to stderr (best-effort, never fails completion).
	if status == "done" {
		touchedPaths, _ := resolveWorkItemPaths(id, dir)
		emitDriftNudge(os.Stderr, touchedPaths, dir, iarch.GitDiffNameOnly)
	}

	// Recap nudge: for code-bearing completions, remind the agent to run recap.
	// Non-fatal, stderr only, consistent with the emitDriftNudge pattern above.
	if status == "done" && shouldAutocommitWorkitemArtifact(typeName) {
		fmt.Fprintf(os.Stderr, "  ! recap: wipnote recap %s   (grounded diff recap of this work)\n", id)
	}

	// Auto plan-rollup recap: when the last feature of a plan completes, generate
	// the plan-rollup recap automatically. Best-effort, non-fatal.
	if status == "done" && typeName == "feature" {
		maybeAutoGeneratePlanRollupRecap(dir, node, p)
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

// wiCompleteStepCmd creates the complete-step subcommand for a work item type.
func wiCompleteStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "complete-step <id> [step-number]",
		Short: "Mark a manual step as completed",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			stepNum := 0
			if len(args) > 1 {
				var err error
				stepNum, err = strconv.Atoi(args[1])
				if err != nil {
					return fmt.Errorf("step number must be an integer, got %q", args[1])
				}
			}
			return runWiCompleteStep(typeName, args[0], stepNum)
		},
	}
}

// runWiCompleteStep completes a manual step by index (1-based) or auto-completes the next incomplete step if stepNum is 0.
func runWiCompleteStep(typeName, id string, stepNum int) error {
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
	if err := col.CompleteStep(id, stepNum); err != nil {
		return fmt.Errorf("complete step: %w", err)
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

// resolveNodeByUnionID resolves a partial or full ID against both live files and
// archived ledger entries. Exact matches (live or archived) win outright and are
// never ambiguous, even if they are also a prefix of other IDs. Prefix matches
// that span both live and archived sources return an ambiguity error. This ensures
// that a prefix matching one live item and one archived item correctly reports
// ambiguity rather than silently picking the live one.
func resolveNodeByUnionID(wipnoteDir, id string) (*models.Node, error) {
	// 1. Check for exact match in live files.
	path := resolveNodePath(wipnoteDir, id)
	if path != "" {
		parsed, parseErr := htmlparse.ParseFile(path)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, parseErr)
		}
		return parsed, nil
	}

	// 2. Check for exact match in archived ledgers.
	archived, archErr := resolveArchivedNode(wipnoteDir, id)
	if archErr != nil {
		return nil, archErr
	}
	if archived != nil {
		return archived, nil
	}

	// 3. No exact match found. Collect all partial matches from live files.
	liveMatches, liveErr := partialLiveMatches(wipnoteDir, id)
	if liveErr != nil {
		return nil, liveErr
	}

	// 4. Collect all partial matches from archived ledgers.
	archivedMatches, archErr := partialArchivedMatches(wipnoteDir, id)
	if archErr != nil {
		return nil, archErr
	}

	// 5. Combine and deduplicate prefix matches.
	allMatches := append(liveMatches, archivedMatches...)
	sort.Strings(allMatches)
	allMatches = dedupMatches(allMatches)

	switch len(allMatches) {
	case 0:
		kind := kindFromPrefix(id)
		return nil, workitem.ErrNotFoundOnDisk(kind, id)
	case 1:
		matched := allMatches[0]
		// Try live file first, then archive.
		path := resolveNodePath(wipnoteDir, matched)
		if path != "" {
			parsed, parseErr := htmlparse.ParseFile(path)
			if parseErr != nil {
				return nil, fmt.Errorf("parse %s: %w", path, parseErr)
			}
			return parsed, nil
		}
		// Not in live files, try archive.
		archived, archErr := resolveArchivedNode(wipnoteDir, matched)
		if archErr != nil {
			return nil, archErr
		}
		if archived != nil {
			return archived, nil
		}
		// Shouldn't happen, but handle gracefully.
		kind := kindFromPrefix(matched)
		return nil, workitem.ErrNotFoundOnDisk(kind, matched)
	default:
		return nil, fmt.Errorf("ambiguous ID %q — did you mean one of: %s",
			id, strings.Join(allMatches, ", "))
	}
}

// partialLiveMatches returns all live IDs that start with prefix, or an error
// if a collection directory cannot be read (except for not-found directories,
// which are skipped). This propagates unexpected scan failures so corruption
// is surfaced rather than silently treated as no-match.
func partialLiveMatches(wipnoteDir, prefix string) ([]string, error) {
	var matches []string
	dirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs"}
	for _, sub := range dirs {
		dir := filepath.Join(wipnoteDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("scan %s: %w", sub, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".html") {
				continue
			}
			stem := strings.TrimSuffix(name, ".html")
			if strings.HasPrefix(stem, prefix) {
				matches = append(matches, stem)
			}
		}
	}
	return matches, nil
}

// partialArchivedMatches returns all archived IDs that start with prefix.
func partialArchivedMatches(wipnoteDir, prefix string) ([]string, error) {
	var matches []string
	for _, col := range graph.ArchiveLedgerCollections {
		entries, err := graph.ReadLedger(graph.ArchiveLedgerPath(wipnoteDir, col))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.ID, prefix) {
				matches = append(matches, e.ID)
			}
		}
	}
	return matches, nil
}

// dedupMatches removes consecutive duplicate strings from a sorted slice.
func dedupMatches(matches []string) []string {
	if len(matches) <= 1 {
		return matches
	}
	unique := make([]string, 1, len(matches))
	unique[0] = matches[0]
	for i := 1; i < len(matches); i++ {
		if matches[i] != matches[i-1] {
			unique = append(unique, matches[i])
		}
	}
	return unique
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

	printRollup(n)

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

// printRollup renders the outcome rollup that Collection.Complete persisted
// into the item's canonical HTML (feat-7ee73444). This is the surface agents
// actually read, so each number is printed next to its provenance rather than
// alone — a cost figure whose source says "degraded_under_report" must not be
// readable as a clean total. Metrics that were omitted for want of data are
// simply not here; the markers (telemetry/git/unavailable) explain why.
func printRollup(n *models.Node) {
	props := workitem.RollupProps(n)
	if len(props) == 0 {
		return
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		if strings.HasSuffix(k, "-source") || strings.HasSuffix(k, "-coverage") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("\nRollup:")
	for _, k := range keys {
		line := fmt.Sprintf("  %-16s %s", k, props[k])
		var qual []string
		if src := props[k+"-source"]; src != "" {
			qual = append(qual, src)
		}
		if cov := props[k+"-coverage"]; cov != "" {
			qual = append(qual, "coverage "+cov)
		}
		if len(qual) > 0 {
			line += "  (" + strings.Join(qual, ", ") + ")"
		}
		fmt.Println(line)
	}
}

// checkProvenanceCompleteGate is the type-agnostic provenance gate for
// code-bearing bug/feature/spike completion. It runs as a pre-completion
// gate (before col.Complete and slice-1's transactional commit) so a
// blocked item is never transitioned to "done".
//
// Decision:
//   - Item not code-bearing (no canonical evidence of a source path outside
//     .wipnote/) → exempt, complete normally.
//   - Code-bearing AND >=1 linked source commit → complete normally.
//   - Code-bearing AND zero linked commits AND no advisory → BLOCK with a
//     non-zero error carrying the --accepted-advisory remediation.
//   - Code-bearing AND zero linked commits AND advisory set → complete,
//     persisting the rationale on the .wipnote artifact (Properties +
//     audit note) and emitting a stderr warning.
//
// Both facts come from canonical storage, never from a read index: linked
// commits are parsed out of git history under wipnote's commit convention and
// code-bearing paths from those commits' diffs, falling back to uncommitted
// working-tree source for an item an agent demonstrably implemented. See
// workitem_provenance_canonical.go for the derivation and its known narrowing.
func checkProvenanceCompleteGate(p *workitem.Project, col *workitem.Collection, typeName, id, advisory string) error {
	repoRoot := filepath.Dir(p.ProjectDir)
	node, _ := col.Get(id)
	commits := canonicalLinkedCommits(repoRoot, id, node)
	codePaths := canonicalCodeBearingPaths(repoRoot, p.ProjectDir, id, node, commits)
	if len(codePaths) == 0 {
		// Pure-.wipnote/doc item — exempt.
		return nil
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

// researchEvidenceMarker prefixes the content note recording the research URL(s)
// / waiver supplied at completion, so audit tooling can surface it.
const researchEvidenceMarker = "research-evidence: "

// wiIsResearchURL reports whether u is a usable http(s) source URL — it must
// parse, carry an http/https scheme, AND have a non-empty host. Mirrors
// plan/planyaml.isResearchURL so the completion gate and the v4 plan gate apply
// the same shape check.
func wiIsResearchURL(u string) bool {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

// checkDependencyResearchCompleteGate enforces the work-item analogue of the v4
// plan research gate (plan/planyaml/validate.go:394-400). When the item's
// code-bearing paths include a dependency manifest (go.mod/package.json/…) — i.e.
// the work changed an external dependency — completion must cite at least one
// http(s) research URL (--research-url) OR record an explicit --research-waiver.
// The cited evidence is persisted on the .wipnote artifact for audit.
//
// Items that touch no dependency manifest are exempt (return nil), so ordinary
// feature/bug completion is unaffected (feat-d1bcbf10 / spk-0a982f70).
func checkDependencyResearchCompleteGate(p *workitem.Project, col *workitem.Collection, typeName, id string, researchURLs []string, researchWaiver string) error {
	// Shape-check EVERY supplied --research-url up front (mirrors the v4 plan
	// validator, which validates the shape of every research URL regardless of
	// enforcement). A single invalid entry is a hard error rather than being
	// silently dropped when another valid URL or a waiver is present
	// (roborev #580).
	for _, u := range researchURLs {
		if !wiIsResearchURL(u) {
			return fmt.Errorf(
				"refusing to complete %s %s: --research-url %q must be an http(s) URL with a host",
				typeName, id, u)
		}
	}

	repoRoot := filepath.Dir(p.ProjectDir)
	node, _ := col.Get(id)
	codePaths := canonicalCodeBearingPaths(repoRoot, p.ProjectDir, id,
		node, canonicalLinkedCommits(repoRoot, id, node))
	var manifests []string
	for _, cp := range codePaths {
		if paths.IsDependencyManifest(cp) {
			manifests = append(manifests, cp)
		}
	}
	if len(manifests) == 0 {
		return nil // no dependency change → exempt
	}

	validURLs := researchURLs // all entries are valid (checked above)
	waiver := strings.TrimSpace(researchWaiver)
	if len(validURLs) == 0 && waiver == "" {
		return fmt.Errorf(
			"refusing to complete %s %s: it changes dependency manifest(s) (%s) but cites no web research.\n"+
				"Pass --research-url <https://…> with the docs/changelog you verified, or --research-waiver \"<reason>\".",
			typeName, id, strings.Join(manifests, ", "))
	}

	// Persist the cited evidence / waiver on the canonical artifact for audit,
	// mirroring how --accepted-advisory records its rationale. Done BEFORE
	// col.Complete so the note survives the completion flush.
	var note string
	if len(validURLs) > 0 {
		note = researchEvidenceMarker + strings.Join(validURLs, ", ")
	}
	if waiver != "" {
		if note != "" {
			note += " | "
		} else {
			note = researchEvidenceMarker
		}
		note += "waiver: " + waiver
	}
	if err := col.Edit(id).AddNote(note).Save(); err != nil {
		return fmt.Errorf("research gate: record research evidence on %s: %w", id, err)
	}
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
	return validateCompletionGateRecord(projectRoot, sessionID, workItemID)
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
