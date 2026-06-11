package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/storage"
)

type gateCommand struct {
	Name string
	Args []string
	// Dir overrides the working directory for this command (guard-profile cwd).
	// Empty means use the plan's ManifestDir.
	Dir string
}

type gatePlan struct {
	ProjectType paths.ProjectType
	ManifestDir string
	Manifest    string
	Commands    []gateCommand
	// UsedProfile is true when the approved guard profile (not manifest
	// autodetection) supplied Commands.
	UsedProfile bool
	// ProfileSignature is the canonical signature of the approved profile when
	// UsedProfile is true; empty otherwise.
	ProfileSignature string
	// GuardNames lists the guard names that ran (profile path only).
	GuardNames []string
}

type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
}

type gateAllowlistEntry struct {
	ID            string   `json:"id"`
	MatchAll      []string `json:"match_all"`
	Justification string   `json:"justification"`
}

type gateAllowlistHit struct {
	ID            string `json:"id"`
	Command       string `json:"command"`
	Justification string `json:"justification"`
}

type gateRunResult struct {
	Plan          gatePlan
	Commands      []string
	Passed        bool
	AllowlistHits []gateAllowlistHit
	OutputSummary string
	Record        *dbpkg.GateRecord
}

// gitWorktreeFacts returns the worktree top-level and the shared git-common-dir
// for dir. Empty strings mean dir is not inside a git worktree (or git failed).
func gitWorktreeFacts(dir string) (top, common string) {
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	top = run("rev-parse", "--show-toplevel")
	common = run("rev-parse", "--git-common-dir")
	if common != "" && !filepath.IsAbs(common) {
		// --git-common-dir may be relative to dir; make it absolute so it can
		// be compared across worktrees.
		if abs, err := filepath.Abs(filepath.Join(dir, common)); err == nil {
			common = abs
		}
	}
	return top, common
}

// resolveCodeRoot decides which directory the gate commands should run in. The
// gate must validate the code actually under test — the worktree the command was
// invoked from — not always projectRoot (where .wipnote/ lives). It overrides to
// the invocation worktree ONLY when that worktree is a linked worktree of the
// SAME repository as projectRoot (shared git-common-dir). Otherwise it returns
// projectRoot, which keeps unrelated callers (and tests whose projectRoot is an
// independent temp dir) running exactly where they expect. Pure for testability.
func resolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon string) string {
	if cwdTop == "" || cwdCommon == "" || projCommon == "" {
		return projectRoot
	}
	if filepath.Clean(cwdTop) == filepath.Clean(projectRoot) {
		return projectRoot
	}
	if filepath.Clean(cwdCommon) == filepath.Clean(projCommon) {
		return cwdTop
	}
	return projectRoot
}

// gateCodeRoot resolves the directory the gate should run in, given the wipnote
// projectRoot. When invoked from a linked worktree of the same repo it returns
// that worktree; otherwise it returns projectRoot. See resolveCodeRoot.
func gateCodeRoot(projectRoot string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return projectRoot
	}
	cwdTop, cwdCommon := gitWorktreeFacts(cwd)
	_, projCommon := gitWorktreeFacts(projectRoot)
	return resolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon)
}

func detectGatePlan(projectRoot, codeRoot, phase string) (gatePlan, error) {
	// Approved guard profile takes precedence over manifest autodetection. The
	// phase selects which guard group runs: PhaseQuality for `check --gate`,
	// PhaseCompletion for the completion re-check (roborev #3703 — completion
	// previously resolved PhaseQuality, so the completion phase never ran). The
	// manifest-autodetection fallback below is phase-agnostic.
	guards, usedProfile, err := guardprofile.ResolveGuards(projectRoot, phase)
	if err != nil {
		return gatePlan{}, fmt.Errorf("resolve guard profile: %w", err)
	}
	if usedProfile {
		prof, _ := guardprofile.Load(projectRoot)
		plan := gatePlan{
			ProjectType:      paths.ProjectTypeUnknown,
			ManifestDir:      codeRoot,
			UsedProfile:      true,
			ProfileSignature: guardprofile.Signature(prof),
		}
		for _, g := range guards {
			dir := codeRoot
			if strings.TrimSpace(g.Cwd) != "" {
				dir = filepath.Join(codeRoot, filepath.FromSlash(g.Cwd))
			}
			plan.Commands = append(plan.Commands, gateCommand{
				Name: g.Name,
				Args: []string{"sh", "-c", g.Cmd},
				Dir:  dir,
			})
			plan.GuardNames = append(plan.GuardNames, g.Name)
		}
		return plan, nil
	}

	manifestDir, manifestName, projectType := detectManifest(codeRoot)
	if projectType == paths.ProjectTypeUnknown {
		// No supported manifest detected — resolve to a zero-command no-op plan
		// that passes trivially. This covers pure-documentation repos and any
		// project layout we do not yet recognise. A DETECTED type (Go/JS/Python/
		// Rust) whose runner is unavailable must still run and may fail; that
		// case is covered by --accepted-advisory, not by this no-op path.
		return gatePlan{
			ProjectType: paths.ProjectTypeUnknown,
			ManifestDir: codeRoot,
		}, nil
	}
	plan := gatePlan{
		ProjectType: projectType,
		ManifestDir: manifestDir,
		Manifest:    filepath.Join(manifestDir, manifestName),
	}
	switch projectType {
	case paths.ProjectTypeGo:
		plan.Commands = []gateCommand{
			{Name: "go build", Args: []string{"go", "build", "-buildvcs=false", "./..."}},
			{Name: "go vet", Args: []string{"go", "vet", "./..."}},
			{Name: "go test", Args: []string{"go", "test", "-buildvcs=false", "./..."}},
		}
	case paths.ProjectTypeNode:
		plan.Commands, err = nodeGateCommands(plan.Manifest)
		if err != nil {
			return gatePlan{}, err
		}
	case paths.ProjectTypePython:
		plan.Commands = []gateCommand{
			{Name: "uv run ruff check .", Args: []string{"uv", "run", "ruff", "check", "."}},
			{Name: "uv run pytest", Args: []string{"uv", "run", "pytest"}},
		}
	case paths.ProjectTypeRust:
		plan.Commands = []gateCommand{
			{Name: "cargo build", Args: []string{"cargo", "build"}},
			{Name: "cargo clippy", Args: []string{"cargo", "clippy"}},
			{Name: "cargo test", Args: []string{"cargo", "test"}},
		}
	default:
		return gatePlan{}, fmt.Errorf("unsupported project type %q", projectType)
	}
	return plan, nil
}

func nodeGateCommands(manifestPath string) ([]gateCommand, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest packageJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(manifestPath), err)
	}
	if len(manifest.Scripts) == 0 {
		return nil, nil
	}
	commands := []gateCommand{}
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{name: "build", args: []string{"npm", "run", "build"}},
		{name: "lint", args: []string{"npm", "run", "lint"}},
		{name: "test", args: []string{"npm", "test"}},
	} {
		if _, ok := manifest.Scripts[candidate.name]; ok {
			commands = append(commands, gateCommand{Name: strings.Join(candidate.args, " "), Args: candidate.args})
		}
	}
	return commands, nil
}

func detectManifest(projectRoot string) (dir, file string, projectType paths.ProjectType) {
	candidates := []struct {
		file string
		typ  paths.ProjectType
	}{
		{"go.mod", paths.ProjectTypeGo},
		{"package.json", paths.ProjectTypeNode},
		{"pyproject.toml", paths.ProjectTypePython},
		{"requirements.txt", paths.ProjectTypePython},
		{"Cargo.toml", paths.ProjectTypeRust},
	}
	dirs := []string{projectRoot}
	for _, sub := range []string{"packages", "src"} {
		entries, err := os.ReadDir(filepath.Join(projectRoot, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(projectRoot, sub, e.Name()))
			}
		}
	}
	for _, candidate := range candidates {
		for _, dir := range dirs {
			if _, err := os.Stat(filepath.Join(dir, candidate.file)); err == nil {
				return dir, candidate.file, candidate.typ
			}
		}
	}
	return "", "", paths.ProjectTypeUnknown
}

func runSessionGate(projectRoot, sessionID, workItemID, source, phase string, stdout, stderr io.Writer) (*gateRunResult, error) {
	// The gate must validate the code under test — the worktree the command was
	// invoked from — not always projectRoot (where .wipnote/ lives). State (DB,
	// guard profile, gate record) still resolves against projectRoot.
	codeRoot := gateCodeRoot(projectRoot)
	if filepath.Clean(codeRoot) != filepath.Clean(projectRoot) {
		fmt.Fprintf(stderr, "gate: running in worktree %s (state in %s)\n", codeRoot, projectRoot)
	}
	plan, err := detectGatePlan(projectRoot, codeRoot, phase)
	if err != nil {
		return nil, err
	}

	// When no approved profile drove the plan, emit a single stderr hint
	// pointing at guard setup so projects can opt into a committed contract.
	if !plan.UsedProfile {
		fmt.Fprintln(stderr, "hint: no approved guard profile found — gate ran via autodetection. Run `wipnote guard init` to define and approve a project guard profile.")
	}

	// No-op path: unknown project type (and no profile) has zero commands and
	// passes trivially. A profile-driven plan with zero matching guards also
	// lands here but is treated as a profile pass.
	if !plan.UsedProfile && plan.ProjectType == paths.ProjectTypeUnknown {
		fmt.Fprintln(stdout, "no supported project manifest detected — treating quality gate as a no-op pass")
		result := &gateRunResult{
			Plan:          plan,
			Passed:        true,
			Commands:      []string{},
			OutputSummary: "no-op: no supported project manifest",
		}
		record, err := persistGateRecord(projectRoot, sessionID, workItemID, source, result)
		if err != nil {
			return nil, err
		}
		result.Record = record
		return result, nil
	}

	allowlist, err := loadGateAllowlist(projectRoot)
	if err != nil {
		return nil, err
	}

	result := &gateRunResult{
		Plan:     plan,
		Passed:   true,
		Commands: make([]string, 0, len(plan.Commands)),
	}
	for _, gc := range plan.Commands {
		result.Commands = append(result.Commands, strings.Join(gc.Args, " "))
	}

	ctx, stop := gateSignalContext()
	defer stop()

	var summary []string
	for _, gc := range plan.Commands {
		hits, cmdErr := runGateCommand(ctx, gc, plan.ManifestDir, allowlist, stdout, stderr)
		if len(hits) > 0 {
			result.AllowlistHits = append(result.AllowlistHits, hits...)
		}
		if cmdErr != nil {
			if gateCommandAllowlisted(cmdErr, hits) {
				summary = append(summary, fmt.Sprintf("%s allowlisted", gc.Name))
				continue
			}
			result.Passed = false
			summary = append(summary, fmt.Sprintf("%s failed", gc.Name))
		}
	}

	if len(result.AllowlistHits) > 0 {
		writeGateAllowlistHits(stdout, result.AllowlistHits)
	}

	if len(summary) == 0 {
		summary = append(summary, "all commands passed")
	}
	result.OutputSummary = strings.Join(summary, "; ")

	record, err := persistGateRecord(projectRoot, sessionID, workItemID, source, result)
	if err != nil {
		return nil, err
	}
	result.Record = record
	return result, nil
}

func writeGateAllowlistHits(w io.Writer, hits []gateAllowlistHit) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment allowlist hits")
	fmt.Fprintln(w, "-------------------------")
	for _, hit := range hits {
		fmt.Fprintf(w, "  - %s (%s)\n", hit.ID, hit.Command)
		fmt.Fprintf(w, "    justification: %s\n", hit.Justification)
	}
}

func runGateCommand(ctx context.Context, gc gateCommand, dir string, allowlist []gateAllowlistEntry, stdout, stderr io.Writer) ([]gateAllowlistHit, error) {
	runDir := dir
	if strings.TrimSpace(gc.Dir) != "" {
		runDir = gc.Dir
	}
	// bug-58205bf3: build an environment whose temp dir is exec-capable. In the
	// devcontainer /tmp is mounted noexec, so `go test` fails to exec the freshly
	// linked test binary; gateExecEnv redirects GOTMPDIR (and GOCACHE if needed)
	// to an exec-capable scratch dir inside the project tree.
	env, _, _ := gateExecEnv(runDir)
	// bug-c3c9278a: run in its own process group with a context watchdog so an
	// interrupt kills the whole `go test`/`go build` subtree instead of orphaning
	// it. runManagedGate also announces "running: <cmd>" for buffered-tty progress.
	output, err := runManagedGate(ctx, gc.Name, runDir, env, stdout, stderr, gc.Args...)
	if err == nil {
		return nil, nil
	}
	if isLikelyNoexecFailure(output) {
		fmt.Fprintf(stderr, "\nhint: this looks like a noexec temp-dir failure. Retry with an exec-capable temp dir:\n  %s\n", gateTmpRemediation(runDir))
	}
	return matchGateAllowlist(gc.Name, output, allowlist), err
}

func gateCommandAllowlisted(cmdErr error, hits []gateAllowlistHit) bool {
	return cmdErr != nil && len(hits) > 0
}

func persistGateRecord(projectRoot, sessionID, workItemID, source string, result *gateRunResult) (*dbpkg.GateRecord, error) {
	if result == nil {
		return nil, fmt.Errorf("gate result is nil")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return nil, fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	hitsJSON, err := json.Marshal(result.AllowlistHits)
	if err != nil {
		return nil, fmt.Errorf("marshal gate allowlist hits: %w", err)
	}
	guardsRun := result.Plan.GuardNames
	if guardsRun == nil {
		guardsRun = []string{}
	}
	guardsRunJSON, err := json.Marshal(guardsRun)
	if err != nil {
		return nil, fmt.Errorf("marshal guards run: %w", err)
	}
	record := &dbpkg.GateRecord{
		SessionID:         sessionID,
		WorkItemID:        workItemID,
		Harness:           currentHarness(),
		ProjectType:       string(result.Plan.ProjectType),
		GateCommand:       strings.Join(result.Commands, " && "),
		Status:            gateStatus(result.Passed),
		CheckedAt:         time.Now().UTC(),
		AllowlistHitsJSON: string(hitsJSON),
		AllowlistHitCount: len(result.AllowlistHits),
		Source:            source,
		OutputSummary:     result.OutputSummary,
		ProfileSignature:  result.Plan.ProfileSignature,
		GuardsRunJSON:     string(guardsRunJSON),
	}
	record.EnsureSignature()
	if err := dbpkg.InsertGateRecord(database, record); err != nil {
		return nil, err
	}
	return record, nil
}

func gateStatus(passed bool) string {
	if passed {
		return "pass"
	}
	return "fail"
}

func loadGateAllowlist(projectRoot string) ([]gateAllowlistEntry, error) {
	path := filepath.Join(projectRoot, "plugin", "config", "quality-gate-flake-allowlist.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load gate allowlist: %w", err)
	}
	var entries []gateAllowlistEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse gate allowlist: %w", err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			return nil, fmt.Errorf("gate allowlist entry missing id")
		}
		if strings.TrimSpace(entry.Justification) == "" {
			return nil, fmt.Errorf("gate allowlist entry %q missing justification", entry.ID)
		}
		if len(entry.MatchAll) == 0 {
			return nil, fmt.Errorf("gate allowlist entry %q missing match_all", entry.ID)
		}
	}
	return entries, nil
}

func matchGateAllowlist(commandName, output string, entries []gateAllowlistEntry) []gateAllowlistHit {
	lower := strings.ToLower(output)
	var hits []gateAllowlistHit
	for _, entry := range entries {
		matched := true
		for _, needle := range entry.MatchAll {
			if !strings.Contains(lower, strings.ToLower(needle)) {
				matched = false
				break
			}
		}
		if matched {
			hits = append(hits, gateAllowlistHit{
				ID:            entry.ID,
				Command:       commandName,
				Justification: entry.Justification,
			})
		}
	}
	return hits
}

func activeWorkItemForGate(sessionID, agentID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return ""
	}
	project, err := storage.CanonicalDBPath(filepath.Dir(wipnoteDir))
	if err != nil {
		return ""
	}
	database, err := dbpkg.OpenReadOnly(project)
	if err != nil {
		return ""
	}
	defer database.Close()
	return dbpkg.GetActiveWorkItemWithFallback(database, sessionID, dbpkg.NormaliseAgentID(agentID))
}

// resolveGateWorkItem determines the work_item_id to attribute a gate run to.
// Resolution order (first non-empty wins):
//  1. Explicit --work-item flag (validated: the work item must exist in the DB).
//  2. Session-scoped attribution via activeWorkItemForGate.
//  3. Last-resort: most recently started in-progress work item for the project.
//
// The resolution path taken is logged to w (one line). When all paths return ""
// the gate still runs but the record is stored with an empty work_item_id,
// preserving existing behaviour.
func resolveGateWorkItem(projectRoot, sessionID, agentID, flagValue string, w io.Writer) string {
	// The DB for paths 1 and 3 is resolved from projectRoot — NOT the process
	// cwd. Resolving via findWipnoteDir() (cwd-relative) picked the wrong
	// repository when the gate ran from a subdirectory or sibling checkout
	// (bug-fddf5820, finding 3). Open the project DB once and reuse it for both
	// the --work-item validation and the most-recent-in-progress fallback.
	database := openProjectGateDB(projectRoot)
	if database != nil {
		defer database.Close()
	}

	// Path 1: explicit --work-item flag. Validate against the DB so a typo or a
	// stale ID surfaces immediately rather than silently recording a gate run
	// against a nonexistent work item (bug-fddf5820, finding 4). When the DB is
	// unavailable we cannot validate, so we trust the flag (preserving prior
	// behaviour for environments without a read index).
	if strings.TrimSpace(flagValue) != "" {
		id := strings.TrimSpace(flagValue)
		if database != nil && !dbpkg.WorkItemExists(database, id) {
			fmt.Fprintf(w, "gate: warning — --work-item %s not found in the project index; recording the run against it anyway\n", id)
		} else {
			fmt.Fprintf(w, "gate: attributing run to work item %s (from --work-item flag)\n", id)
		}
		return id
	}

	// Path 2: session-scoped attribution.
	if id := activeWorkItemForGate(sessionID, agentID); id != "" {
		fmt.Fprintf(w, "gate: attributing run to work item %s (session-scoped claim)\n", id)
		return id
	}

	// Path 3: last-resort — most recent in-progress item for the project.
	if database != nil {
		if id := dbpkg.MostRecentInProgressWorkItem(database); id != "" {
			fmt.Fprintf(w, "gate: attributing run to work item %s (most recent in-progress — session attribution not available)\n", id)
			return id
		}
	}

	fmt.Fprintln(w, "gate: no active work item resolved; gate record will have empty work_item_id")
	return ""
}

// openProjectGateDB opens the read index for projectRoot's canonical DB path.
// Returns nil (rather than erroring) when the path cannot be resolved or the
// DB cannot be opened — callers degrade gracefully to non-DB behaviour. The DB
// is anchored on projectRoot so attribution does not depend on the process cwd
// (bug-fddf5820, finding 3).
func openProjectGateDB(projectRoot string) *sql.DB {
	if strings.TrimSpace(projectRoot) == "" {
		return nil
	}
	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		return nil
	}
	database, err := dbpkg.OpenReadOnly(dbPath)
	if err != nil {
		return nil
	}
	return database
}

// reportGuardProfileDrift prints a READ-ONLY stale/needs-revalidation notice
// when the latest recorded gate's profile_signature differs from the current
// approved guard-profile signature. It NEVER fails completion or mutates the
// work item — drift is cleared by re-running the gate (which re-records the
// fresh signature) or by re-approving the profile. No-ops silently when there
// is no approved profile, no prior record, or the signatures already match.
func reportGuardProfileDrift(database *sql.DB, projectRoot, sessionID string, w io.Writer) {
	if database == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	prof, err := guardprofile.Load(projectRoot)
	if err != nil || prof == nil || !guardprofile.IsApproved(prof) {
		return
	}
	current := guardprofile.Signature(prof)

	record, err := dbpkg.LatestGateRecordForSession(database, sessionID)
	if err != nil || record == nil {
		return
	}
	// Only meaningful when the prior record was itself profile-driven.
	if record.ProfileSignature == "" || record.ProfileSignature == current {
		return
	}
	fmt.Fprintf(w, "notice: guard-profile drift — last passing gate recorded profile %s but the approved profile is now %s. The gate will be re-run to revalidate against the current contract.\n",
		record.ProfileSignature, current)
}

// completionGateFallbackWindow bounds how old a cross-session passing gate
// record may be to satisfy the bug-35857288 fallback. It is generous enough to
// cover a normal review/merge cycle but short enough that a stale record cannot
// silently authorise a much-later completion. The mandatory immediate re-check
// at current HEAD remains the real safety boundary regardless of this window.
const completionGateFallbackWindow = 6 * time.Hour

func validateCompletionGateRecord(projectRoot string, database *sql.DB, sessionID, workItemID string) error {
	if database == nil {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("refusing to complete %s: no active session id is available; run `wipnote check --gate` from an active session first", workItemID)
	}

	record, err := dbpkg.LatestGateRecordForSession(database, sessionID)
	if err != nil {
		return fmt.Errorf("load gate record: %w", err)
	}
	sessionScopedValid := record != nil && record.Status == "pass" && record.SignatureValid()
	if !sessionScopedValid {
		// bug-35857288: cross-session fallback. A work item validated by a
		// passing gate in another session (e.g. the merge was prepared and
		// gated in a sibling worktree/session) must still be completable here,
		// rather than being hard-rejected for lacking a session-scoped record.
		// Accept the most recent passing record for THIS work item within a
		// recency window. HEAD safety is not taken on trust from the prior
		// record: completion below ALWAYS re-runs the gate at the current HEAD
		// in this session and requires a fresh valid passing record, so the
		// fallback only relaxes the "must have a prior same-session record"
		// precondition — it never substitutes for HEAD-current validation.
		fallback, ferr := dbpkg.LatestPassingGateRecordForWorkItem(database, workItemID, completionGateFallbackWindow)
		if ferr != nil {
			return fmt.Errorf("load gate record: %w", ferr)
		}
		if fallback == nil || !fallback.SignatureValid() {
			return fmt.Errorf("refusing to complete %s: no valid passing gate record exists for the current session (%s) and no recent passing gate record (within %s) exists for this work item.\nRun:\n  wipnote check --gate", workItemID, sessionID, completionGateFallbackWindow)
		}
		fmt.Fprintf(os.Stderr, "gate: accepting cross-session passing gate record for %s from session %s (checked %s ago); re-validating at current HEAD before completing\n",
			workItemID, fallback.SessionID, time.Since(fallback.CheckedAt).Round(time.Second))
	}

	result, err := runSessionGate(projectRoot, sessionID, workItemID, "complete-recheck", guardprofile.PhaseCompletion, os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("re-run quality gate before completing %s: %w", workItemID, err)
	}
	if result == nil || result.Record == nil || result.Record.Status != "pass" || !result.Record.SignatureValid() {
		return fmt.Errorf("refusing to complete %s: the immediate gate re-check did not produce a valid passing record for session %s", workItemID, sessionID)
	}
	return nil
}
