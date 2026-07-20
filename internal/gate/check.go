package gate

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

type Command struct {
	Name string
	Args []string
	Dir  string
}

// DurabilityContentionFixture names the always-on durability regression test
// that proves the single-writer-daemon fix (plan-2390966a, feat-bbb80917):
// under a held external write lock, every migrated hot hook routes its
// derived-index write enqueue-only and completes in <1s, with ZERO first-party
// SQLITE_BUSY across three consecutive runs.
//
// The fixture is deliberately NOT skipped in -short mode, so the Go quality
// gate's `go test ... -short ./...` step (built in DetectPlan below) exercises
// it on every gate run — a regression that reverted a hot hook to a direct
// writable Exec (re-introducing the stall) FAILS the gate. This constant is the
// load-bearing reference: gate_durability_test.go asserts the Go gate plan runs
// the package that hosts the fixture under -short, so deleting either the
// reference or the always-on property fails CI.
const (
	// DurabilityContentionFixturePkg is the Go package (relative to the module
	// root) whose tests include the durability contention fixture.
	DurabilityContentionFixturePkg = "./cmd/wipnote/"

	// DurabilityContentionFixtureTest is the test function that enforces the
	// sub-second / zero-first-party-BUSY durability invariant under a held lock.
	DurabilityContentionFixtureTest = "TestSQLiteContentionStress_MigratedHotHooksUnderHeldLock"

	// GoTestTimeoutArg is the canonical -timeout flag for the Go quality gate.
	// It is a single source of truth used by every Go test gate invocation
	// (autodetect plan, runGoGates non-gate path) so all paths carry the same
	// timeout and no path silently omits it (bug-a8ae8cd7).
	GoTestTimeoutArg = "-timeout=300s"
)

// GoGateRunsDurabilityFixtureUnderShort reports whether the supplied gate
// commands include a `go test` invocation that (a) runs in -short mode and
// (b) covers the whole module (`./...`), and therefore exercises the always-on
// durability contention fixture (DurabilityContentionFixtureTest), which does
// NOT skip under -short. A regression in the migrated hot hooks consequently
// fails the standard quality gate.
//
// Two command shapes are recognized:
//   - Autodetected Go plan: argv-form `["go", "test", "-short", "./...", ...]`.
//   - Approved guard profile: shell-form `["sh", "-c", "go test -short ./..."]`.
//     DetectPlan renders every profile guard as `sh -c <g.Cmd>`, so the
//     durability step in a project's .wipnote/guard-profile.yaml quality phase
//     is matched here too (roborev-476 finding 4). This is what lets
//     gate_durability_test.go assert the REAL approved-profile path this repo
//     uses still gates the fixture, not only the synthetic autodetected plan.
func GoGateRunsDurabilityFixtureUnderShort(commands []Command) bool {
	for _, c := range commands {
		if goTestArgvRunsShortAll(c.Args) || shellCmdRunsGoTestShortAll(c.Args) {
			return true
		}
	}
	return false
}

// goTestArgvRunsShortAll matches the autodetected argv form
// `["go", "test", ... "-short" ... "./..." ...]`.
func goTestArgvRunsShortAll(args []string) bool {
	if len(args) < 2 || args[0] != "go" || args[1] != "test" {
		return false
	}
	hasShort, hasAll := false, false
	for _, a := range args[2:] {
		switch {
		case a == "-short" || a == "--short":
			hasShort = true
		case a == "./...":
			hasAll = true
		}
	}
	return hasShort && hasAll
}

// shellCmdRunsGoTestShortAll matches the approved-profile shell form
// `["sh", "-c", "<shell>"]` where the shell string contains a `go test`
// invocation running -short over ./.... The check is conservative: it requires
// "go test", a -short / --short flag, and the ./... module-wide selector all
// present in the same shell command string.
func shellCmdRunsGoTestShortAll(args []string) bool {
	if len(args) < 3 || args[0] != "sh" || args[1] != "-c" {
		return false
	}
	shell := args[2]
	if !strings.Contains(shell, "go test") {
		return false
	}
	hasShort := strings.Contains(shell, " -short") || strings.Contains(shell, " --short")
	hasAll := strings.Contains(shell, "./...")
	return hasShort && hasAll
}

// ProjectTypeElixir identifies an Elixir/mix project for the quality gate
// (bug-1b2b1529, #153). It is intentionally scoped to this package rather
// than added to the shared core/paths.ProjectType marker table: the fix is
// specific to `wipnote check --gate`, and adding it to the shared table would
// also change behavior for unrelated paths.DetectProjectType consumers (the
// yolo per-commit task-completion gate, `wipnote init`) which is out of scope
// for this fix.
const ProjectTypeElixir paths.ProjectType = "elixir"

type Plan struct {
	ProjectType      paths.ProjectType
	ManifestDir      string
	Manifest         string
	Commands         []Command
	UsedProfile      bool
	ProfileSignature string
	GuardNames       []string
}

type AllowlistEntry struct {
	ID            string   `json:"id"`
	MatchAll      []string `json:"match_all"`
	Justification string   `json:"justification"`
}

type AllowlistHit struct {
	ID            string `json:"id"`
	Command       string `json:"command"`
	Justification string `json:"justification"`
}

type RunResult struct {
	Plan          Plan
	Commands      []string
	Passed        bool
	// Skipped reports that no gate commands were run because no supported
	// project manifest (or approved guard profile) was detected. A skipped
	// run is NEVER recorded as a passing gate (bug-1b2b1529, #153): Passed is
	// always false alongside Skipped=true, so gateStatus persists "fail" (the
	// gate_records status column only allows pass/fail) and callers that care
	// about the skip/fail distinction can inspect this in-memory field.
	Skipped       bool
	AllowlistHits []AllowlistHit
	OutputSummary string
	Record        *dbpkg.GateRecord
}

type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
}

type RunOptions struct {
	ProjectRoot string
	SessionID   string
	WorkItemID  string
	Source      string
	Phase       string
	Harness     string
	Stdout      io.Writer
	Stderr      io.Writer
}

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
		if abs, err := filepath.Abs(filepath.Join(dir, common)); err == nil {
			common = abs
		}
	}
	// Canonicalize both facts to their real (symlink-resolved) path. On
	// macOS, TMPDIR — and therefore Go's testing.T.TempDir() — resolves
	// through /var -> /private/var. `git rev-parse --git-common-dir` invoked
	// from the MAIN worktree's own directory reports the plain relative
	// ".git" (joined above onto the caller's possibly non-canonical `dir`
	// argument), while the same query from a LINKED worktree comes back
	// already resolved to the real absolute path. Left uncanonicalized, two
	// semantically-identical common dirs compare unequal by literal string
	// in ResolveCodeRoot — a pre-existing bug this fixes (surfaced by
	// TestCodeRoot_LinkedWorktreeOverride flaking on macOS). EvalSymlinks is
	// best-effort: a nonexistent/unreadable path (e.g. the non-repo test
	// cases) silently falls back to the unresolved value.
	if resolved, err := filepath.EvalSymlinks(top); err == nil && resolved != "" {
		top = resolved
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil && resolved != "" {
		common = resolved
	}
	return top, common
}

func ResolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon string) string {
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

func CodeRoot(projectRoot string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return projectRoot
	}
	cwdTop, cwdCommon := gitWorktreeFacts(cwd)
	_, projCommon := gitWorktreeFacts(projectRoot)
	return ResolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon)
}

func DetectPlan(projectRoot, codeRoot, phase string) (Plan, error) {
	guards, usedProfile, err := guardprofile.ResolveGuards(projectRoot, phase)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve guard profile: %w", err)
	}
	if usedProfile {
		prof, _ := guardprofile.Load(projectRoot)
		plan := Plan{
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
			plan.Commands = append(plan.Commands, Command{
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
		return Plan{ProjectType: paths.ProjectTypeUnknown, ManifestDir: codeRoot}, nil
	}
	plan := Plan{ProjectType: projectType, ManifestDir: manifestDir, Manifest: filepath.Join(manifestDir, manifestName)}
	switch projectType {
	case paths.ProjectTypeGo:
		// The `go test ... -short ./...` step covers the whole module, which
		// includes the always-on durability contention fixture
		// (DurabilityContentionFixtureTest in DurabilityContentionFixturePkg).
		// That fixture is intentionally NOT skipped under -short, so a
		// regression in the migrated hot hooks (plan-2390966a, feat-bbb80917)
		// — e.g. reverting a hot write to a direct, lock-contending Exec —
		// fails THIS gate. GoGateRunsDurabilityFixtureUnderShort + the
		// gate_durability_test.go assertion guard that this command keeps both
		// the -short flag and the module-wide ./... scope.
		plan.Commands = []Command{
			{Name: "go build", Args: []string{"go", "build", "-buildvcs=false", "./..."}},
			{Name: "go vet", Args: []string{"go", "vet", "./..."}},
			{Name: "go test", Args: []string{"go", "test", "-buildvcs=false", "-short", GoTestTimeoutArg, "./..."}},
		}
	case paths.ProjectTypeNode:
		plan.Commands, err = NodeGateCommands(plan.Manifest)
		if err != nil {
			return Plan{}, err
		}
	case paths.ProjectTypePython:
		plan.Commands = []Command{
			{Name: "uv run ruff check .", Args: []string{"uv", "run", "ruff", "check", "."}},
			{Name: "uv run pytest", Args: []string{"uv", "run", "pytest"}},
		}
	case paths.ProjectTypeRust:
		plan.Commands = []Command{
			{Name: "cargo build", Args: []string{"cargo", "build"}},
			{Name: "cargo clippy", Args: []string{"cargo", "clippy"}},
			{Name: "cargo test", Args: []string{"cargo", "test"}},
		}
	case ProjectTypeElixir:
		// mix compile --warnings-as-errors / mix test / mix format
		// --check-formatted are the standard Elixir CI trio (bug-1b2b1529,
		// #153): compile fails the build on any warning, mix test runs the
		// suite, and format --check-formatted fails if any source file is
		// not `mix format`-clean. See https://hexdocs.pm/mix/Mix.Tasks.Format.html.
		plan.Commands = []Command{
			{Name: "mix compile", Args: []string{"mix", "compile", "--warnings-as-errors"}},
			{Name: "mix test", Args: []string{"mix", "test"}},
			{Name: "mix format --check-formatted", Args: []string{"mix", "format", "--check-formatted"}},
		}
	default:
		return Plan{}, fmt.Errorf("unsupported project type %q", projectType)
	}
	return plan, nil
}

func NodeGateCommands(manifestPath string) ([]Command, error) {
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
	commands := []Command{}
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{name: "build", args: []string{"npm", "run", "build"}},
		{name: "lint", args: []string{"npm", "run", "lint"}},
		{name: "test", args: []string{"npm", "test"}},
	} {
		if _, ok := manifest.Scripts[candidate.name]; ok {
			commands = append(commands, Command{Name: strings.Join(candidate.args, " "), Args: candidate.args})
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
		{"mix.exs", ProjectTypeElixir},
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

func RunSession(opts RunOptions) (*RunResult, error) {
	if opts.WorkItemID != "" {
		os.Setenv("WIPNOTE_WORKITEM_ID", opts.WorkItemID)
		defer os.Unsetenv("WIPNOTE_WORKITEM_ID")
	}
	if opts.SessionID != "" {
		os.Setenv("WIPNOTE_SESSION_ID", opts.SessionID)
		defer os.Unsetenv("WIPNOTE_SESSION_ID")
	}

	codeRoot := CodeRoot(opts.ProjectRoot)
	if filepath.Clean(codeRoot) != filepath.Clean(opts.ProjectRoot) {
		fmt.Fprintf(opts.Stderr, "gate: running in worktree %s (state in %s)\n", codeRoot, opts.ProjectRoot)
	}
	plan, err := DetectPlan(opts.ProjectRoot, codeRoot, opts.Phase)
	if err != nil {
		return nil, err
	}
	if !plan.UsedProfile {
		fmt.Fprintln(opts.Stderr, "hint: no approved guard profile found — gate ran via autodetection. Run `wipnote guard init` to define and approve a project guard profile.")
	}
	if !plan.UsedProfile && plan.ProjectType == paths.ProjectTypeUnknown {
		// bug-1b2b1529 (#153): a manifest-less project used to record a
		// silent PASS here, giving false confidence that the gate had
		// checked something. WARN loudly and record a distinct "skipped"
		// status instead — a no-op can no longer be mistaken for a green
		// gate (gateStatus never maps Skipped to "pass").
		fmt.Fprintln(opts.Stderr, "WARN: no supported project manifest detected (looked for go.mod, package.json, pyproject.toml, requirements.txt, Cargo.toml, mix.exs) — quality gate SKIPPED, not passed. Run `wipnote guard init` to define an explicit guard profile if this project uses an unsupported/custom build.")
		result := &RunResult{Plan: plan, Passed: false, Skipped: true, Commands: []string{}, OutputSummary: "skipped: no supported project manifest detected"}
		record, err := PersistRecord(opts.ProjectRoot, opts.SessionID, opts.WorkItemID, opts.Source, opts.Harness, result)
		if err != nil {
			return nil, err
		}
		result.Record = record
		return result, nil
	}

	allowlist, err := LoadAllowlist(opts.ProjectRoot)
	if err != nil {
		return nil, err
	}

	result := &RunResult{Plan: plan, Passed: true, Commands: make([]string, 0, len(plan.Commands))}
	for _, gc := range plan.Commands {
		result.Commands = append(result.Commands, strings.Join(gc.Args, " "))
	}

	ctx, stop := SignalContext()
	defer stop()

	var summary []string
	for _, gc := range plan.Commands {
		hits, cmdErr := runGateCommand(ctx, gc, plan.ManifestDir, allowlist, opts.Stdout, opts.Stderr)
		if len(hits) > 0 {
			result.AllowlistHits = append(result.AllowlistHits, hits...)
		}
		if cmdErr != nil {
			if GateCommandAllowlisted(cmdErr, hits) {
				summary = append(summary, fmt.Sprintf("%s allowlisted", gc.Name))
				continue
			}
			result.Passed = false
			summary = append(summary, fmt.Sprintf("%s failed", gc.Name))
		}
	}

	if len(result.AllowlistHits) > 0 {
		WriteAllowlistHits(opts.Stdout, result.AllowlistHits)
	}
	if len(summary) == 0 {
		summary = append(summary, "all commands passed")
	}
	result.OutputSummary = strings.Join(summary, "; ")

	record, err := PersistRecord(opts.ProjectRoot, opts.SessionID, opts.WorkItemID, opts.Source, opts.Harness, result)
	if err != nil {
		return nil, err
	}
	result.Record = record
	return result, nil
}

func WriteAllowlistHits(w io.Writer, hits []AllowlistHit) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment allowlist hits")
	fmt.Fprintln(w, "-------------------------")
	for _, hit := range hits {
		fmt.Fprintf(w, "  - %s (%s)\n", hit.ID, hit.Command)
		fmt.Fprintf(w, "    justification: %s\n", hit.Justification)
	}
}

func runGateCommand(ctx context.Context, gc Command, dir string, allowlist []AllowlistEntry, stdout, stderr io.Writer) ([]AllowlistHit, error) {
	runDir := dir
	if strings.TrimSpace(gc.Dir) != "" {
		runDir = gc.Dir
	}
	env, _, _ := GateExecEnv(runDir)
	output, err := RunManagedGate(ctx, gc.Name, runDir, env, stdout, stderr, gc.Args...)
	if err == nil {
		return nil, nil
	}
	if IsLikelyNoexecFailure(output) {
		fmt.Fprintf(stderr, "\nhint: this looks like a noexec temp-dir failure. Retry with an exec-capable temp dir:\n  %s\n", GateTmpRemediation(runDir))
	}
	return MatchAllowlist(gc.Name, output, allowlist), err
}

func GateCommandAllowlisted(cmdErr error, hits []AllowlistHit) bool {
	return cmdErr != nil && len(hits) > 0
}

func PersistRecord(projectRoot, sessionID, workItemID, source, harness string, result *RunResult) (*dbpkg.GateRecord, error) {
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
		Harness:           harness,
		ProjectType:       string(result.Plan.ProjectType),
		GateCommand:       strings.Join(result.Commands, " && "),
		Status:            gateStatus(result),
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

// gateStatus derives the persisted DB status for a gate run. The gate_records
// table's status column is constrained to ('pass','fail') (see
// core/db/schema.go), so a Skipped result (no supported manifest / no
// approved guard profile) persists as "fail" — this still satisfies
// bug-1b2b1529 (#153): "fail" can never be mistaken for a passing gate, and
// RunResult.Skipped plus the loud stderr WARN (see RunSession) preserve the
// distinction between "the gate ran and found problems" and "the gate never
// ran anything" for callers that inspect the in-memory RunResult (e.g. the
// `check --gate` CLI's tailored error message) rather than only the DB row.
func gateStatus(result *RunResult) string {
	if result.Passed {
		return "pass"
	}
	return "fail"
}

func LoadAllowlist(projectRoot string) ([]AllowlistEntry, error) {
	path := filepath.Join(projectRoot, "plugin", "config", "quality-gate-flake-allowlist.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load gate allowlist: %w", err)
	}
	var entries []AllowlistEntry
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

func MatchAllowlist(commandName, output string, entries []AllowlistEntry) []AllowlistHit {
	lower := strings.ToLower(output)
	var hits []AllowlistHit
	for _, entry := range entries {
		matched := true
		for _, needle := range entry.MatchAll {
			if !strings.Contains(lower, strings.ToLower(needle)) {
				matched = false
				break
			}
		}
		if matched {
			hits = append(hits, AllowlistHit{ID: entry.ID, Command: commandName, Justification: entry.Justification})
		}
	}
	return hits
}

func ActiveWorkItemForGate(projectRoot, sessionID, agentID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	database := openProjectGateDB(projectRoot)
	if database == nil {
		return ""
	}
	defer database.Close()
	return dbpkg.GetActiveWorkItemWithFallback(database, sessionID, dbpkg.NormaliseAgentID(agentID))
}

func ResolveWorkItem(projectRoot, sessionID, agentID, flagValue string, w io.Writer) string {
	database := openProjectGateDB(projectRoot)
	if database != nil {
		defer database.Close()
	}
	if strings.TrimSpace(flagValue) != "" {
		id := strings.TrimSpace(flagValue)
		if database != nil && !dbpkg.WorkItemExists(database, id) {
			fmt.Fprintf(w, "gate: warning — --work-item %s not found in the project index; recording the run against it anyway\n", id)
		} else {
			fmt.Fprintf(w, "gate: attributing run to work item %s (from --work-item flag)\n", id)
		}
		return id
	}
	if id := ActiveWorkItemForGate(projectRoot, sessionID, agentID); id != "" {
		fmt.Fprintf(w, "gate: attributing run to work item %s (session-scoped claim)\n", id)
		return id
	}
	if database != nil {
		if id := dbpkg.MostRecentInProgressWorkItem(database); id != "" {
			fmt.Fprintf(w, "gate: attributing run to work item %s (most recent in-progress — session attribution not available)\n", id)
			return id
		}
	}
	fmt.Fprintln(w, "gate: no active work item resolved; gate record will have empty work_item_id")
	return ""
}

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

func ReportGuardProfileDrift(database *sql.DB, projectRoot, sessionID string, w io.Writer) {
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
	if record.ProfileSignature == "" || record.ProfileSignature == current {
		return
	}
	fmt.Fprintf(w, "notice: guard-profile drift — last passing gate recorded profile %s but the approved profile is now %s. The gate will be re-run to revalidate against the current contract.\n", record.ProfileSignature, current)
}

const CompletionGateFallbackWindow = 6 * time.Hour

func ValidateCompletionRecord(projectRoot string, database *sql.DB, sessionID, workItemID, harness string, stdout, stderr io.Writer) error {
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
		fallback, ferr := dbpkg.LatestPassingGateRecordForWorkItem(database, workItemID, CompletionGateFallbackWindow)
		if ferr != nil {
			return fmt.Errorf("load gate record: %w", ferr)
		}
		if fallback == nil || !fallback.SignatureValid() {
			return fmt.Errorf("refusing to complete %s: no valid passing gate record exists for the current session (%s) and no recent passing gate record (within %s) exists for this work item.\nRun:\n  wipnote check --gate", workItemID, sessionID, CompletionGateFallbackWindow)
		}
		fmt.Fprintf(stderr, "gate: accepting cross-session passing gate record for %s from session %s (checked %s ago); re-validating at current HEAD before completing\n", workItemID, fallback.SessionID, time.Since(fallback.CheckedAt).Round(time.Second))
	}
	result, err := RunSession(RunOptions{ProjectRoot: projectRoot, SessionID: sessionID, WorkItemID: workItemID, Source: "complete-recheck", Phase: guardprofile.PhaseCompletion, Harness: harness, Stdout: stdout, Stderr: stderr})
	if err != nil {
		return fmt.Errorf("re-run quality gate before completing %s: %w", workItemID, err)
	}
	if result == nil || result.Record == nil || result.Record.Status != "pass" || !result.Record.SignatureValid() {
		return fmt.Errorf("refusing to complete %s: the immediate gate re-check did not produce a valid passing record for session %s", workItemID, sessionID)
	}
	return nil
}
