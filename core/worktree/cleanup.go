package worktree

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultEmptySpikeCleanupTTL = 7 * 24 * time.Hour
	snapshotRefPrefix           = "refs/wipnote-snapshots/"
)

type CleanupConfig struct {
	EmptySpikeWorktreeCleanup bool
	EmptySpikeWorktreeTTLDays int
}

type cleanupConfigFile struct {
	EmptySpikeWorktreeCleanup bool `json:"empty_spike_worktree_cleanup"`
	EmptySpikeWorktreeTTLDays int  `json:"empty_spike_worktree_ttl_days"`
}

func (c CleanupConfig) Enabled() bool {
	return c.EmptySpikeWorktreeCleanup
}

func (c CleanupConfig) TTL() time.Duration {
	if c.EmptySpikeWorktreeTTLDays <= 0 {
		return defaultEmptySpikeCleanupTTL
	}
	return time.Duration(c.EmptySpikeWorktreeTTLDays) * 24 * time.Hour
}

func LoadCleanupConfig(projectDir string) CleanupConfig {
	cfg := CleanupConfig{
		EmptySpikeWorktreeCleanup: false,
		EmptySpikeWorktreeTTLDays: int(defaultEmptySpikeCleanupTTL / (24 * time.Hour)),
	}
	if projectDir == "" {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return cfg
	}
	var fileCfg cleanupConfigFile
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return cfg
	}
	cfg.EmptySpikeWorktreeCleanup = fileCfg.EmptySpikeWorktreeCleanup
	if fileCfg.EmptySpikeWorktreeTTLDays > 0 {
		cfg.EmptySpikeWorktreeTTLDays = fileCfg.EmptySpikeWorktreeTTLDays
	}
	return cfg
}

type CleanupState struct {
	WorktreePath      string
	Branch            string
	HeadSHA           string
	HasTrackedChanges bool
	HasUntrackedFiles bool
	HasUniqueCommits  bool
	Locked            bool
	LockReason        string
	SnapshotRef       string
	SnapshotPatchPath string
}

func (s CleanupState) Removable() bool {
	return !s.Locked && !s.HasTrackedChanges && !s.HasUntrackedFiles && !s.HasUniqueCommits
}

func InspectCleanupState(repoRoot, worktreePath string) (CleanupState, error) {
	state := CleanupState{WorktreePath: worktreePath}
	if repoRoot == "" || worktreePath == "" {
		return state, fmt.Errorf("inspect cleanup state: repoRoot and worktreePath are required")
	}
	if !isUnderDir(worktreePath, filepath.Join(repoRoot, ".claude", "worktrees")) {
		return state, fmt.Errorf("inspect cleanup state: %s is outside managed worktrees", worktreePath)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		return state, fmt.Errorf("inspect cleanup state: stat %s: %w", worktreePath, err)
	}

	branch, _ := gitOutput(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	state.Branch = strings.TrimSpace(branch)
	headSHA, err := gitOutput(worktreePath, "rev-parse", "HEAD")
	if err == nil {
		state.HeadSHA = strings.TrimSpace(headSHA)
	}

	status, err := gitOutput(worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return state, fmt.Errorf("inspect cleanup state: git status: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "?? ") {
			state.HasUntrackedFiles = true
			continue
		}
		if strings.TrimSpace(line) != "" {
			state.HasTrackedChanges = true
		}
	}
	if err := scanner.Err(); err != nil {
		return state, fmt.Errorf("inspect cleanup state: parse git status: %w", err)
	}

	locked, reason, err := worktreeLockStatus(worktreePath)
	if err != nil {
		return state, fmt.Errorf("inspect cleanup state: lock status: %w", err)
	}
	state.Locked = locked
	state.LockReason = reason

	if state.HeadSHA != "" {
		containsOut, err := gitOutput(
			repoRoot,
			"for-each-ref",
			"--format=%(refname:short)",
			"--contains",
			state.HeadSHA,
			"refs/heads",
			"refs/remotes",
		)
		if err != nil {
			return state, fmt.Errorf("inspect cleanup state: refs containing HEAD: %w", err)
		}
		var elsewhere bool
		for line := range strings.SplitSeq(strings.TrimSpace(containsOut), "\n") {
			ref := strings.TrimSpace(line)
			if ref == "" {
				continue
			}
			if ref == state.Branch || ref == "HEAD" {
				continue
			}
			elsewhere = true
			break
		}
		state.HasUniqueCommits = !elsewhere
	}

	return state, nil
}

func SnapshotPreservedWorktree(repoRoot, worktreePath, workItemID string, state CleanupState) (CleanupState, error) {
	if repoRoot == "" || worktreePath == "" || workItemID == "" {
		return state, fmt.Errorf("snapshot preserved worktree: repoRoot, worktreePath, and workItemID are required")
	}
	headSHA := state.HeadSHA
	if headSHA == "" {
		out, err := gitOutput(worktreePath, "rev-parse", "HEAD")
		if err == nil {
			headSHA = strings.TrimSpace(out)
			state.HeadSHA = headSHA
		}
	}

	shortSHA := "nohead"
	if len(headSHA) >= 12 {
		shortSHA = headSHA[:12]
	} else if headSHA != "" {
		shortSHA = headSHA
	}

	refName := snapshotRefPrefix + sanitizeSnapshotToken(workItemID) + "-" + sanitizeSnapshotToken(shortSHA)
	if headSHA != "" {
		existing, _ := gitOutput(repoRoot, "rev-parse", "--verify", refName)
		if strings.TrimSpace(existing) == headSHA {
			state.SnapshotRef = refName
			return state, nil
		}
		if _, err := gitOutput(repoRoot, "update-ref", refName, headSHA); err == nil {
			state.SnapshotRef = refName
			return state, nil
		}
	}

	patchDir := filepath.Join(repoRoot, ".wipnote", "worktree-snapshots")
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		return state, fmt.Errorf("snapshot preserved worktree: mkdir %s: %w", patchDir, err)
	}
	patchPath := filepath.Join(patchDir, sanitizeSnapshotToken(workItemID)+"-"+sanitizeSnapshotToken(shortSHA)+".patch")
	patchOut, err := gitOutput(worktreePath, "diff", "--binary", "HEAD")
	if err != nil {
		return state, fmt.Errorf("snapshot preserved worktree: git diff: %w", err)
	}
	if patchOut == "" {
		patchOut = "# snapshot fallback\n# no tracked diff available; worktree preserved on disk\n"
	}
	if err := os.WriteFile(patchPath, []byte(patchOut), 0o644); err != nil {
		return state, fmt.Errorf("snapshot preserved worktree: write %s: %w", patchPath, err)
	}
	state.SnapshotPatchPath = patchPath
	return state, nil
}

func RemoveManagedWorktree(repoRoot, worktreePath string) error {
	if repoRoot == "" || worktreePath == "" {
		return fmt.Errorf("remove managed worktree: repoRoot and worktreePath are required")
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove managed worktree %s: %w\n%s", worktreePath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func worktreeLockStatus(worktreePath string) (bool, string, error) {
	gitFile := filepath.Join(worktreePath, ".git")
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir: ") {
		return false, "", nil
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktreePath, gitdir)
	}
	lockedPath := filepath.Join(gitdir, "locked")
	data, err := os.ReadFile(lockedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	return true, strings.TrimSpace(string(data)), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %w (%s)", args, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func sanitizeSnapshotToken(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}
