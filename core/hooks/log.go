package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// debugLog writes a diagnostic message to .wipnote/debug.log if it can be resolved.
// Silently no-ops if the project dir can't be found or the file can't be opened.
func debugLog(projectDir, format string, args ...any) {
	if projectDir == "" {
		return
	}
	logPath := filepath.Join(projectDir, ".wipnote", "debug.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02T15:04:05"), msg)
}

// debugLogFields writes a structured log line with key=value pairs to debug.log.
// Format: 2006-01-02T15:04:05 handler=<h> <key=value ...> <msg>
// Fields are sorted by key for deterministic output. Silently no-ops when
// projectDir is empty or the log file cannot be opened.
func debugLogFields(projectDir, handler string, fields map[string]string, msg string) {
	if projectDir == "" {
		return
	}
	logPath := filepath.Join(projectDir, ".wipnote", "debug.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var sb strings.Builder
	sb.WriteString(time.Now().Format("2006-01-02T15:04:05"))
	sb.WriteString(" handler=")
	sb.WriteString(handler)

	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteByte(' ')
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(fields[k])
		}
	}

	if msg != "" {
		sb.WriteByte(' ')
		sb.WriteString(msg)
	}
	sb.WriteByte('\n')
	f.WriteString(sb.String()) //nolint:errcheck
}

// LogError logs a handler error with structured context (handler name, session ID).
// It resolves the project dir from env/CWD so it can be called from cmd/wipnote
// where projectDir is not yet known. Silently no-ops if the project cannot be found.
func LogError(handler, sessionID, msg string) {
	projectDir := resolveLogDir()
	if projectDir == "" {
		return
	}
	fields := map[string]string{}
	if sessionID != "" {
		fields["session"] = sessionID[:minSessionLen(sessionID)]
	}
	debugLogFields(projectDir, handler, fields, "[error] "+msg)
}

// LogTimed writes a structured log line including elapsed duration since start.
// Convenience wrapper for timing call sites — adds a "duration" field automatically.
// fields may be nil; a new map is allocated internally if so.
func LogTimed(projectDir, handler string, fields map[string]string, start time.Time, msg string) {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["duration"] = time.Since(start).String()
	debugLogFields(projectDir, handler, fields, msg)
}

// resolveLogDir finds the project directory for logging by checking env then CWD walk-up.
func resolveLogDir() string {
	cwd, _ := os.Getwd()
	return ResolveProjectDir(cwd, "")
}

// MinSessionLen returns min(8, len(s)) for safe session ID truncation in log messages.
// Exported so cmd/wipnote can use it when building structured log fields.
func MinSessionLen(s string) int {
	if len(s) < 8 {
		return len(s)
	}
	return 8
}

// minSessionLen is the unexported alias kept for internal callers.
func minSessionLen(s string) int { return MinSessionLen(s) }
