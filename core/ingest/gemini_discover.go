package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// geminiProjects is the structure of ~/.gemini/projects.json.
type geminiProjects struct {
	Projects map[string]string `json:"projects"`
}

// DiscoverGeminiSessions scans ~/.gemini/tmp/<slug>/chats/ for Gemini CLI
// session JSON files. If projectFilter is non-empty, only sessions for matching
// project paths or slugs are returned.
func DiscoverGeminiSessions(projectFilter string) ([]GeminiSessionFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	geminiDir := filepath.Join(home, ".gemini")
	if _, err := os.Stat(geminiDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("gemini directory not found at %s\nGemini CLI must be installed and have been run at least once. Install from https://gemini.google.com/cli", geminiDir)
	}

	// Load the projects map to resolve path → slug.
	projectsMap, err := loadGeminiProjects(geminiDir)
	if err != nil {
		// projects.json may not exist; proceed with tmp dir scan.
		projectsMap = map[string]string{}
	}

	tmpDir := filepath.Join(geminiDir, "tmp")
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		return nil, nil // no sessions yet
	}

	slugFilter := resolveGeminiSlugFilter(projectFilter, projectsMap)

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read gemini tmp dir: %w", err)
	}

	var files []GeminiSessionFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		if slugFilter != "" && !strings.EqualFold(slug, slugFilter) {
			continue
		}

		chatsDir := filepath.Join(tmpDir, slug, "chats")
		jsonFiles, _ := filepath.Glob(filepath.Join(chatsDir, "*.json"))
		for _, f := range jsonFiles {
			// Skip per-session subdirectory index files that are not full sessions.
			// Session files start with "session-" or are UUID-named.
			base := filepath.Base(f)
			if !isGeminiSessionFile(base) {
				continue
			}

			info, _ := os.Stat(f)
			size := int64(0)
			if info != nil {
				size = info.Size()
			}

			sessionID := extractGeminiSessionID(base)
			files = append(files, GeminiSessionFile{
				Path:      f,
				SessionID: sessionID,
				Project:   slug,
				Size:      size,
			})
		}
	}

	return files, nil
}

// loadGeminiProjects reads ~/.gemini/projects.json and returns the path→slug map.
func loadGeminiProjects(geminiDir string) (map[string]string, error) {
	path := filepath.Join(geminiDir, "projects.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var gp geminiProjects
	if err := json.Unmarshal(data, &gp); err != nil {
		return nil, fmt.Errorf("parse projects.json: %w", err)
	}
	if gp.Projects == nil {
		return map[string]string{}, nil
	}
	return gp.Projects, nil
}

// resolveGeminiSlugFilter converts a project path or name filter into the
// corresponding Gemini project slug. Returns "" if no filter or not resolvable.
func resolveGeminiSlugFilter(projectFilter string, projectsMap map[string]string) string {
	if projectFilter == "" {
		return ""
	}
	// Check if projectFilter is an absolute path matching a known project.
	for path, slug := range projectsMap {
		if strings.EqualFold(path, projectFilter) ||
			strings.HasPrefix(strings.ToLower(path), strings.ToLower(projectFilter)+"/") {
			return slug
		}
	}
	// Treat projectFilter as a direct slug name.
	return projectFilter
}

// isGeminiSessionFile returns true if the filename looks like a Gemini session
// JSON file (as opposed to a logs.json or similar auxiliary file).
func isGeminiSessionFile(base string) bool {
	if base == "logs.json" {
		return false
	}
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	return true
}

// extractGeminiSessionID derives a session ID from a Gemini session filename.
// For "session-2026-04-12T01-03-4a8d77d4.json" → "4a8d77d4".
// For UUID-named files like "4a8d77d4-0033-4f19-b3c0-9d2fb1791c06.json" → full UUID.
func extractGeminiSessionID(base string) string {
	name := strings.TrimSuffix(base, ".json")
	if strings.HasPrefix(name, "session-") {
		// Format: session-YYYY-MM-DDTHH-MM-<short-id>
		// Extract the short ID after the last dash.
		parts := strings.Split(name, "-")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	// UUID or other format — return as-is.
	return name
}
