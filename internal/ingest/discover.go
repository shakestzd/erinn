package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/paths"
)

// SessionFile represents a discovered Claude Code JSONL session file.
type SessionFile struct {
	Path         string
	SessionID    string // UUID extracted from filename
	Project      string // decoded project name
	GitRemoteURL string // git remote origin URL of the project dir (may be empty)
	Size         int64
}

// DiscoverSessions scans ~/.claude/projects/ for JSONL session files.
// If projectFilter is non-empty, only files under matching project dirs are returned.
func DiscoverSessions(projectFilter string) ([]SessionFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("claude projects directory not found at %s\nClaude Code must be installed and have been run at least once. Install from https://claude.ai/code", projectsDir)
	}

	var files []SessionFile

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, fmt.Errorf("read projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectName := decodeProjectName(entry.Name())
		if projectFilter != "" {
			// Match against decoded path (full filesystem path) so callers can
			// pass CWD directly.  Accept exact matches and subdirectories of
			// the project (worktrees like .claude/worktrees/trk-xxx).  Do NOT
			// accept ancestors of the project — that match direction caused
			// every project's DB to ingest sessions from ~/.claude/projects/
			// -Users-shakes/ (the home-directory entry), because every project
			// lives under the home directory (bug-a52d5bf9).
			decodedPath := decodeProjectPath(entry.Name())
			pathMatch := strings.EqualFold(decodedPath, projectFilter) ||
				strings.HasPrefix(strings.ToLower(decodedPath), strings.ToLower(projectFilter)+"/")
			// Name match is also tightened: only accept exact project-basename
			// matches or clean contains; the old contains-substring check let
			// stray projects through.
			nameMatch := strings.EqualFold(projectName, filepath.Base(projectFilter))
			if !pathMatch && !nameMatch {
				continue
			}
		}

		// Resolve the actual filesystem path so we can query the git remote.
		projectPath := decodeProjectPath(entry.Name())
		remoteURL := paths.GetGitRemoteURL(projectPath)

		projDir := filepath.Join(projectsDir, entry.Name())
		jsonlFiles, _ := filepath.Glob(filepath.Join(projDir, "*.jsonl"))
		for _, f := range jsonlFiles {
			base := filepath.Base(f)
			sessionID := strings.TrimSuffix(base, ".jsonl")
			info, _ := os.Stat(f)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			files = append(files, SessionFile{
				Path:         f,
				SessionID:    sessionID,
				Project:      projectName,
				GitRemoteURL: remoteURL,
				Size:         size,
			})
		}
	}

	return files, nil
}

// DiscoverSubagents finds subagent JSONL files for a given session directory.
func DiscoverSubagents(sessionDir string) ([]SessionFile, error) {
	subagentsDir := filepath.Join(sessionDir, "subagents")
	if _, err := os.Stat(subagentsDir); os.IsNotExist(err) {
		return nil, nil // no subagents
	}

	var files []SessionFile
	jsonlFiles, _ := filepath.Glob(filepath.Join(subagentsDir, "agent-*.jsonl"))
	for _, f := range jsonlFiles {
		base := filepath.Base(f)
		agentID := strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
		info, _ := os.Stat(f)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		files = append(files, SessionFile{
			Path:      f,
			SessionID: agentID,
			Size:      size,
		})
	}
	return files, nil
}

// decodeProjectName converts Claude's dash-encoded path to a human-friendly name.
// e.g. "-Users-shakes-DevProjects-htmlgraph" → "htmlgraph"
func decodeProjectName(encoded string) string {
	parts := strings.Split(encoded, "-")
	if len(parts) == 0 {
		return encoded
	}

	// Look for known parent markers and return the component after them.
	markers := []string{"DevProjects", "code", "projects", "repos", "src", "work", "dev"}
	for i, p := range parts {
		for _, m := range markers {
			if strings.EqualFold(p, m) && i+1 < len(parts) {
				return strings.Join(parts[i+1:], "-")
			}
		}
	}

	// Fallback: return last non-empty component.
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return encoded
}

// decodeProjectPath reconstructs the full filesystem path from Claude Code's
// dash-encoded project directory name.
// Claude encodes paths by replacing "/" with "-", so the leading "-" represents
// the root separator.
// e.g. "-Users-alice-DevProjects-htmlgraph" → "/Users/alice/DevProjects/htmlgraph"
func decodeProjectPath(encoded string) string {
	if encoded == "" {
		return ""
	}
	// Strip a leading dash (represents the root "/") then replace remaining dashes.
	// We replace "-" with "/" which reconstructs the path from the encoding.
	// Claude encodes absolute paths starting with "/" as "-..." so:
	// "-Users-alice-DevProjects-htmlgraph" → "/Users/alice/DevProjects/htmlgraph"
	if strings.HasPrefix(encoded, "-") {
		return "/" + strings.ReplaceAll(encoded[1:], "-", "/")
	}
	return strings.ReplaceAll(encoded, "-", "/")
}

// FilterByGitRemote returns only those SessionFiles whose GitRemoteURL matches
// targetRemote.  If targetRemote is empty the original slice is returned
// unchanged (no filtering).  Sessions with an empty GitRemoteURL are excluded
// when targetRemote is non-empty.
func FilterByGitRemote(files []SessionFile, targetRemote string) []SessionFile {
	if targetRemote == "" {
		return files
	}
	out := files[:0:0] // reuse underlying array type but start fresh
	for _, sf := range files {
		if sf.GitRemoteURL == targetRemote {
			out = append(out, sf)
		}
	}
	return out
}
