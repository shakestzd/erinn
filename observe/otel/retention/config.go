package retention

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the disk-retention knobs read from .wipnote/config.json.
// All values are optional; zero / missing fields fall back to the defaults
// below. This mirrors the local os.ReadFile(.wipnote/config.json) +
// decode-only-what-you-need pattern used by readTaskCompletionConfig
// (internal/hooks) and LivenessStalenessThreshold (internal/db) — there is no
// shared internal/config package in wipnote.
type Config struct {
	// LogMaxBytes is the size cap for a single rotating log file
	// (serve-auto.log, serve-<id>.log, debug.log). When a log exceeds this it
	// is rotated (oldest dropped) and truncated in place.
	LogMaxBytes int64 `json:"log_max_bytes"`
	// LogKeep is the number of rotated copies to keep (e.g. serve-auto.log.1).
	LogKeep int `json:"log_keep"`
	// NDJSONRetainDays is the age (by mtime) beyond which an inactive,
	// fully-ingested events.ndjson is archived+pruned by the retention sweep.
	NDJSONRetainDays int `json:"ndjson_retain_days"`
	// NDJSONMaxSessions, when > 0, caps the number of live (unarchived) session
	// dirs; the oldest inactive+ingested sessions beyond this count are pruned
	// even if younger than NDJSONRetainDays.
	NDJSONMaxSessions int `json:"ndjson_max_sessions"`
}

// Defaults. Conservative on purpose — the sweep only ever touches data that is
// BOTH inactive and durably ingested into SQLite, so age bounds are a secondary
// guard, not the primary safety mechanism.
const (
	DefaultLogMaxBytes      int64 = 50 * 1024 * 1024 // 50 MB
	DefaultLogKeep                = 2
	DefaultNDJSONRetainDays       = 30
	// activeGrace is the recent-mtime window: an events.ndjson modified within
	// this window is treated as possibly-active and never pruned, independent of
	// DB status. Cheap, harness-agnostic liveness guard.
	activeGraceMinutes = 10
)

// LoadConfig reads retention knobs from .wipnote/config.json under projectDir.
// Missing file, unreadable file, malformed JSON, or absent keys all fall back
// to defaults — retention must never fail because config is unavailable.
func LoadConfig(projectDir string) Config {
	cfg := Config{
		LogMaxBytes:       DefaultLogMaxBytes,
		LogKeep:           DefaultLogKeep,
		NDJSONRetainDays:  DefaultNDJSONRetainDays,
		NDJSONMaxSessions: 0,
	}
	if projectDir == "" {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".wipnote", "config.json"))
	if err != nil {
		return cfg
	}
	var raw Config
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg
	}
	if raw.LogMaxBytes > 0 {
		cfg.LogMaxBytes = raw.LogMaxBytes
	}
	if raw.LogKeep > 0 {
		cfg.LogKeep = raw.LogKeep
	}
	if raw.NDJSONRetainDays > 0 {
		cfg.NDJSONRetainDays = raw.NDJSONRetainDays
	}
	if raw.NDJSONMaxSessions > 0 {
		cfg.NDJSONMaxSessions = raw.NDJSONMaxSessions
	}
	return cfg
}
