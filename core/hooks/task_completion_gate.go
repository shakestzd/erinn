package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/guardprofile"
)

// BlockExit2Error is a sentinel error that signals the hook runner to exit
// with code 2 (block) after writing the message to stderr. Claude Code
// interprets exit code 2 as "hook blocks this action".
type BlockExit2Error struct {
	Message string
}

func (e *BlockExit2Error) Error() string {
	return fmt.Sprintf("hook blocked: %s", e.Message)
}

// taskCompletionGateTimeout is the maximum time the quality gate command
// may run before it is killed and the gate fails open (warn-only).
const taskCompletionGateTimeout = 60 * time.Second

// taskCompletionGateResult holds the outcome of a quality gate run.
type taskCompletionGateResult struct {
	Passed   bool
	GateName string
	Output   string
}

// runTaskCompletionGate detects the project type and runs the canonical test
// command. Returns a warn-only result on timeout (fails open to avoid
// stranding teammates indefinitely).
func runTaskCompletionGate(projectDir string) taskCompletionGateResult {
	// Approved guard profile takes precedence over manifest autodetection. The
	// yolo phase is the per-commit gate this hook enforces.
	guards, usedProfile, err := guardprofile.ResolveGuards(projectDir, guardprofile.PhaseYolo)
	if err != nil {
		// A present-but-unreadable/unparseable profile is a configuration problem
		// the user must fix — do NOT silently fall back to autodetection and pass
		// (roborev #3684): surface it as a failing gate result instead.
		return taskCompletionGateResult{
			Passed:   false,
			GateName: "guard-profile-error",
			Output:   fmt.Sprintf("guard profile could not be resolved: %v", err),
		}
	}
	if usedProfile {
		return runGuardProfileGate(projectDir, guards)
	}

	pt := paths.DetectProjectType(projectDir)
	testCmd := paths.TestCommandFor(pt)
	if testCmd == "" {
		return taskCompletionGateResult{Passed: true, GateName: "unknown-project-type"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), taskCompletionGateTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", testCmd)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return taskCompletionGateResult{
			Passed:   true, // fail open on timeout
			GateName: testCmd,
			Output:   "TIMEOUT: quality gate exceeded 60s, proceeding (warn-only)",
		}
	}

	return taskCompletionGateResult{
		Passed:   err == nil,
		GateName: testCmd,
		Output:   string(out),
	}
}

// runGuardProfileGate runs each resolved guard command (respecting per-guard
// cwd) under the shared completion-gate timeout, failing open on timeout to
// match the autodetection path. An empty guard set is a trivial pass.
func runGuardProfileGate(projectDir string, guards []guardprofile.Guard) taskCompletionGateResult {
	if len(guards) == 0 {
		return taskCompletionGateResult{Passed: true, GateName: "guard-profile (no matching guards)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskCompletionGateTimeout)
	defer cancel()

	var combined string
	for _, g := range guards {
		dir := projectDir
		if filepath.IsAbs(g.Cwd) {
			dir = g.Cwd
		} else if g.Cwd != "" {
			dir = filepath.Join(projectDir, filepath.FromSlash(g.Cwd))
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", g.Cmd)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		combined += "$ " + g.Cmd + "\n" + string(out)

		if ctx.Err() == context.DeadlineExceeded {
			return taskCompletionGateResult{
				Passed:   true, // fail open on timeout
				GateName: "guard-profile:" + g.Name,
				Output:   "TIMEOUT: guard profile gate exceeded 60s, proceeding (warn-only)",
			}
		}
		if err != nil {
			return taskCompletionGateResult{
				Passed:   false,
				GateName: "guard-profile:" + g.Name,
				Output:   combined,
			}
		}
	}
	return taskCompletionGateResult{Passed: true, GateName: "guard-profile", Output: combined}
}

// SpecEnforcement holds opt-in spec-presence gate flags. Both default to
// false; existing projects keep their current behavior unchanged until they
// explicitly opt in.
type SpecEnforcement struct {
	PromoteSlice    bool `json:"promote_slice"`
	FeatureComplete bool `json:"feature_complete"`
}

// taskCompletionConfig represents the relevant fields from .wipnote/config.json.
type taskCompletionConfig struct {
	BlockOnQualityFailure bool            `json:"block_task_completion_on_quality_failure"`
	SpecEnforcement       SpecEnforcement `json:"spec_enforcement"`
}

// readTaskCompletionConfig reads the opt-in flag from .wipnote/config.json.
// Returns false (do not block) when the file is missing, unreadable, or the
// key is absent.
func readTaskCompletionConfig(projectDir string) bool {
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return false
	}
	var cfg taskCompletionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.BlockOnQualityFailure
}

// ReadSpecEnforcement returns the opt-in spec_enforcement settings from
// .wipnote/config.json. Returns the zero value (both gates disabled) when
// the file is missing, unreadable, or the key is absent — preserving
// backward-compatible default-off behavior.
func ReadSpecEnforcement(projectDir string) SpecEnforcement {
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return SpecEnforcement{}
	}
	var cfg taskCompletionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return SpecEnforcement{}
	}
	return cfg.SpecEnforcement
}
