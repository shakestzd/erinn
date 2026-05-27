package main

import (
	"bytes"
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

	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/guardprofile"
	"github.com/shakestzd/wipnote/internal/paths"
	"github.com/shakestzd/wipnote/internal/storage"
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

func detectGatePlan(projectRoot, phase string) (gatePlan, error) {
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
			ManifestDir:      projectRoot,
			UsedProfile:      true,
			ProfileSignature: guardprofile.Signature(prof),
		}
		for _, g := range guards {
			dir := projectRoot
			if strings.TrimSpace(g.Cwd) != "" {
				dir = filepath.Join(projectRoot, filepath.FromSlash(g.Cwd))
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

	manifestDir, manifestName, projectType := detectManifest(projectRoot)
	if projectType == paths.ProjectTypeUnknown {
		// No supported manifest detected — resolve to a zero-command no-op plan
		// that passes trivially. This covers pure-documentation repos and any
		// project layout we do not yet recognise. A DETECTED type (Go/JS/Python/
		// Rust) whose runner is unavailable must still run and may fail; that
		// case is covered by --accepted-advisory, not by this no-op path.
		return gatePlan{
			ProjectType: paths.ProjectTypeUnknown,
			ManifestDir: projectRoot,
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
		plan.Commands = []gateCommand{
			{Name: "npm run build", Args: []string{"npm", "run", "build"}},
			{Name: "npm run lint", Args: []string{"npm", "run", "lint"}},
			{Name: "npm test", Args: []string{"npm", "test"}},
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
	plan, err := detectGatePlan(projectRoot, phase)
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

	var summary []string
	for _, gc := range plan.Commands {
		hits, cmdErr := runGateCommand(gc, plan.ManifestDir, allowlist, stdout, stderr)
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

func runGateCommand(gc gateCommand, dir string, allowlist []gateAllowlistEntry, stdout, stderr io.Writer) ([]gateAllowlistHit, error) {
	cmd := exec.Command(gc.Args[0], gc.Args[1:]...)
	if strings.TrimSpace(gc.Dir) != "" {
		cmd.Dir = gc.Dir
	} else {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(stdout, &output)
	cmd.Stderr = io.MultiWriter(stderr, &output)
	err := cmd.Run()
	if err == nil {
		return nil, nil
	}
	return matchGateAllowlist(gc.Name, output.String(), allowlist), err
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
	if record == nil || record.Status != "pass" || !record.SignatureValid() {
		return fmt.Errorf("refusing to complete %s: no valid passing gate record exists for the current session (%s).\nRun:\n  wipnote check --gate", workItemID, sessionID)
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
