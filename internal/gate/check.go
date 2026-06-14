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
		plan.Commands = []Command{
			{Name: "go build", Args: []string{"go", "build", "-buildvcs=false", "./..."}},
			{Name: "go vet", Args: []string{"go", "vet", "./..."}},
			{Name: "go test", Args: []string{"go", "test", "-buildvcs=false", "-short", "./..."}},
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
		fmt.Fprintln(opts.Stdout, "no supported project manifest detected — treating quality gate as a no-op pass")
		result := &RunResult{Plan: plan, Passed: true, Commands: []string{}, OutputSummary: "no-op: no supported project manifest"}
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
