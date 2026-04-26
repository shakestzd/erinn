package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// effectiveProjectDir resolves the project dir for OTel port derivation.
// Priority: explicit arg → CLAUDE_PROJECT_DIR → HTMLGRAPH_PROJECT_DIR → os.Getwd.
func effectiveProjectDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, k := range []string{"CLAUDE_PROJECT_DIR", "HTMLGRAPH_PROJECT_DIR"} {
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
// inherits the user's shell env) and layers HtmlGraph-specific overrides
// on top:
//
//  1. HTMLGRAPH_PROJECT_DIR — set when the launcher runs inside a
//     worktree, so hooks resolve to the main .htmlgraph/ directory.
//  2. OTel exporter vars — enabled by default (default-on). Set
//     HTMLGRAPH_OTEL_ENABLED=0 to opt out. User-set OTel vars win:
//     we never clobber an explicit OTEL_* choice.
//
// htmlgraphProjectDir is the empty string when no override is needed
// (not in a worktree). Pass it explicitly rather than deriving it from
// opts so the helper stays easy to unit-test.
//
// overrides supplies optional per-session collector port and session ID.
// Pass nil or a zero-value struct to use defaults.
func buildClaudeLaunchEnv(htmlgraphProjectDir string, overrides *otelEnvOverrides) []string {
	env := os.Environ()

	// Resolve an effective projectDir for OTel port derivation.
	// Priority chain: explicit arg → CLAUDE_PROJECT_DIR → HTMLGRAPH_PROJECT_DIR → os.Getwd.
	projectDir := effectiveProjectDir(htmlgraphProjectDir)
	if projectDir != "" {
		env = setOrReplaceEnv(env, "HTMLGRAPH_PROJECT_DIR", projectDir)
	}

	// Inject session ID when provided by the collector spawn path.
	if overrides != nil && overrides.SessionID != "" {
		env = setOrReplaceEnv(env, "HTMLGRAPH_SESSION_ID", overrides.SessionID)
	}

	// OTel injection is default-on. Opt out by setting HTMLGRAPH_OTEL_ENABLED=0
	// (or false/no/off). An unset or empty value means "on".
	if isExplicitlyDisabled(os.Getenv("HTMLGRAPH_OTEL_ENABLED")) {
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

	// User-set values always win — only add our default if missing.
	env = addIfUnset(env, "CLAUDE_CODE_ENABLE_TELEMETRY", "1")
	env = addIfUnset(env, "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "1")
	env = addIfUnset(env, "OTEL_METRICS_EXPORTER", "otlp")
	env = addIfUnset(env, "OTEL_LOGS_EXPORTER", "otlp")
	env = addIfUnset(env, "OTEL_TRACES_EXPORTER", "otlp")
	env = addIfUnset(env, "OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	// The launcher's computed endpoint must win because it's derived from the same
	// HTMLGRAPH_OTEL_* config the receiver reads in LoadConfigFromEnv. Inherited env
	// values from a parent session whose hash resolved to a different port would silently
	// drop spans. Users who need to point Claude Code at a non-htmlgraph receiver can
	// steer via HTMLGRAPH_OTEL_HTTP_PORT / HTMLGRAPH_OTEL_BIND.
	env = setOrReplaceEnv(env, "OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	// Tool details include bash commands, skill names, MCP tool names —
	// non-sensitive by default. Turn off by setting to "0" before launch.
	env = addIfUnset(env, "OTEL_LOG_TOOL_DETAILS", "1")
	env = addIfUnset(env, "OTEL_LOG_USER_PROMPTS", "1")
	env = addIfUnset(env, "OTEL_LOG_TOOL_CONTENT", "1")

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
	fmt.Fprintf(os.Stderr, "htmlgraph: warning: OTel receiver at %s is not reachable — Claude Code spans will be dropped. Start htmlgraph serve or HTMLGRAPH_OTEL_HTTP_PORT is wrong.\n", hostport)
}
