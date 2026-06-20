package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/harness"
)

// effectiveProjectDir resolves the project dir for OTel port derivation.
// Priority: explicit arg → CLAUDE_PROJECT_DIR → WIPNOTE_PROJECT_DIR → os.Getwd.
func effectiveProjectDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, k := range []string{"CLAUDE_PROJECT_DIR", "WIPNOTE_PROJECT_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// buildClaudeLaunchEnv returns the environment vector for a spawned
// `claude` process. It always starts from os.Environ() (so the child
// inherits the user's shell env) and layers wipnote-specific overrides
// on top:
//
//  1. WIPNOTE_PROJECT_DIR — set when the launcher runs inside a
//     worktree, so hooks resolve to the main .wipnote/ directory.
//  2. Harness-specific launch env from the registry (e.g.
//     CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1). User-set values win.
//  3. OTel exporter vars — enabled by default (default-on). Set
//     WIPNOTE_OTEL_ENABLED=0 to opt out. User-set OTel vars win:
//     we never clobber an explicit OTEL_* choice.
//
// wipnoteProjectDir is the empty string when no override is needed
// (not in a worktree). Pass it explicitly rather than deriving it from
// opts so the helper stays easy to unit-test.
//
// overrides supplies optional per-session collector port and session ID.
// Pass nil or a zero-value struct to use defaults.
func buildClaudeLaunchEnv(wipnoteProjectDir string, overrides *otelEnvOverrides) []string {
	env := os.Environ()

	// Strip stale inherited launcher session vars before layering fresh ones.
	// A parent Claude shell may export WIPNOTE_SESSION_ID / WIPNOTE_OTEL_SESSION_ID;
	// passing those through would leak a stale identity into the child. The
	// SessionStart hook repopulates WIPNOTE_SESSION_ID with the real session, and
	// WIPNOTE_OTEL_SESSION_ID is set below only for a freshly spawned collector
	// (bug-b262d303).
	env = removeEnv(env, "WIPNOTE_SESSION_ID")
	env = removeEnv(env, "WIPNOTE_OTEL_SESSION_ID")

	// Resolve an effective projectDir for OTel port derivation.
	// Priority chain: explicit arg → CLAUDE_PROJECT_DIR → WIPNOTE_PROJECT_DIR → os.Getwd.
	projectDir := effectiveProjectDir(wipnoteProjectDir)
	if projectDir != "" {
		env = setOrReplaceEnv(env, "WIPNOTE_PROJECT_DIR", projectDir)
	}

	// Inject OTel session ID under its own var — NOT WIPNOTE_SESSION_ID.
	// WIPNOTE_SESSION_ID carries the real Claude Code session identity
	// (populated by the SessionStart hook via writeEnvVars). Mixing the
	// OTel collector's 28-char hex ID into WIPNOTE_SESSION_ID caused
	// launcher_continue.go to pass that ID to `claude --resume`, which
	// has no transcript for it (bug-b262d303).
	if overrides != nil && overrides.SessionID != "" {
		env = setOrReplaceEnv(env, "WIPNOTE_OTEL_SESSION_ID", overrides.SessionID)
	}

	// Layer harness-specific launch env vars from the registry. User-set values
	// always win — we use addIfUnset, matching the OTel injection policy above.
	cfg := harness.Get("claude_code")
	if cfg != nil {
		for _, kv := range cfg.LaunchEnv {
			if idx := strings.Index(kv, "="); idx > 0 {
				env = addIfUnset(env, kv[:idx], kv[idx+1:])
			}
		}
	}

	// OTel injection is default-on. Opt out by setting WIPNOTE_OTEL_ENABLED=0
	// (or false/no/off). An unset or empty value means "on".
	if isExplicitlyDisabled(os.Getenv("WIPNOTE_OTEL_ENABLED")) {
		return env
	}

	// When a per-session collector is running, point the exporter at its
	// ephemeral port. Without a collector, the embedded receiver has been
	// removed from serve — skip OTel endpoint injection entirely so
	// telemetry isn't sent to a dead port.
	var endpoint string
	if overrides != nil && overrides.CollectorPort > 0 {
		endpoint = "http://127.0.0.1:" + strconv.Itoa(overrides.CollectorPort)
	} else {
		return env // no collector, no embedded receiver — skip OTel injection
	}

	// Pull defaults from the registry (centralized per-harness config).
	if cfg != nil {
		otelVars := cfg.OtelEnv(overrides.CollectorPort, "")
		for _, kv := range otelVars {
			idx := strings.Index(kv, "=")
			if idx <= 0 {
				continue
			}
			key, value := kv[:idx], kv[idx+1:]
			if key == "OTEL_EXPORTER_OTLP_ENDPOINT" {
				// Force-replace: launcher's endpoint must win because it's derived
				// from WIPNOTE_OTEL_HTTP_PORT and inherited values would silently
				// drop spans. Users who need to point Claude Code at a non-wipnote
				// receiver can steer via WIPNOTE_OTEL_HTTP_PORT / WIPNOTE_OTEL_BIND.
				env = setOrReplaceEnv(env, key, value)
			} else {
				env = addIfUnset(env, key, value)
			}
		}
	}

	// Probe receiver reachability — print a warning if unreachable. Does not block launch.
	probeReceiverReachability(endpoint)

	return env
}

// addIfUnset appends key=value to env only when key is not already set
// to a non-empty value. This keeps non-empty user overrides authoritative
// while filling gaps with our defaults. An empty string is treated as
// "unset" because Claude Code itself sets several OTEL_* vars to empty
// when spawning subprocesses (observed empirically in the TRACEPARENT
// validation run) — if we respected those as authoritative choices, we'd
// never enable telemetry in a nested launcher.
func addIfUnset(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if len(kv) > len(prefix) {
				return env // non-empty user value wins
			}
			// Empty value — treat as unset and overwrite in place.
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// setOrReplaceEnv replaces the value of key if present, appending
// otherwise. Different from addIfUnset: used for vars where the launcher's
// authoritative intent should override any inherited value (e.g. worktree
// project dir override).
func setOrReplaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// removeEnv returns env with every "KEY=..." entry for key removed. Used to
// strip stale inherited vars before layering fresh launcher values.
func removeEnv(env []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			result = append(result, kv)
		}
	}
	return result
}

// isTruthy matches the parsing used by receiver.LoadConfigFromEnv.
// Kept local here to avoid exporting a helper from the receiver package
// for one env-var check.
func isTruthy(s string) bool {
	switch s {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}

// isExplicitlyDisabled reports whether a value explicitly opts OUT of OTel
// (for the default-on launcher policy). Empty / unset values default to on.
func isExplicitlyDisabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// probeReceiverReachability attempts a 1-second TCP dial to the OTLP endpoint.
// If the dial fails, prints a warning to stderr. Does not block launch.
// If the endpoint is malformed, silently skips the probe.
func probeReceiverReachability(endpoint string) {
	// Parse endpoint to extract host:port. Expect format "http://host:port" or "https://host:port".
	var hostport string
	if strings.HasPrefix(endpoint, "http://") {
		hostport = strings.TrimPrefix(endpoint, "http://")
	} else if strings.HasPrefix(endpoint, "https://") {
		hostport = strings.TrimPrefix(endpoint, "https://")
	} else {
		// Malformed endpoint — skip probe silently. The launcher isn't responsible
		// for validating user overrides.
		return
	}

	conn, err := net.DialTimeout("tcp", hostport, 1*time.Second)
	if err == nil {
		conn.Close()
		return // Reachable — no warning needed.
	}

	// Print warning to stderr. One line only, no logging noise.
	fmt.Fprintf(os.Stderr, "wipnote: warning: OTel receiver at %s is not reachable — Claude Code spans will be dropped. Start wipnote serve or WIPNOTE_OTEL_HTTP_PORT is wrong.\n", hostport)
}
