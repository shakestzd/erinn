package main

import (
	"strings"
	"testing"
)

func assertEnvContains(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, prefix); ok {
			if after != want {
				t.Errorf("%s = %q, want %q", key, after, want)
			}
			return
		}
	}
	t.Errorf("%s not set; want %q", key, want)
}

func assertEnvEmptyOrUnset(t *testing.T, env []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, prefix); ok {
			if after != "" {
				t.Errorf("%s = %q, want empty or unset", key, after)
			}
			return
		}
	}
}

func clearOtelEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WIPNOTE_OTEL_ENABLED",
		"WIPNOTE_OTEL_HTTP_PORT",
		"WIPNOTE_OTEL_BIND",
		"WIPNOTE_OTEL_SESSION_ID",
		"WIPNOTE_SESSION_ID",
		"WIPNOTE_PROJECT_DIR",
		"CLAUDE_PROJECT_DIR",
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA",
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS",
		"OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER",
		"OTEL_TRACES_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_LOG_TOOL_DETAILS",
	} {
		t.Setenv(key, "")
	}
}

func testOverrides(port int) *otelEnvOverrides {
	return &otelEnvOverrides{CollectorPort: port, SessionID: "test-session"}
}

func TestBuildClaudeLaunchEnv_ExplicitOptOut(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("WIPNOTE_OTEL_ENABLED", "0")
	env := buildClaudeLaunchEnv("", nil)
	for _, key := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
	} {
		assertEnvEmptyOrUnset(t, env, key)
	}

	for _, val := range []string{"false", "no", "off"} {
		t.Setenv("WIPNOTE_OTEL_ENABLED", val)
		env = buildClaudeLaunchEnv("", nil)
		assertEnvEmptyOrUnset(t, env, "CLAUDE_CODE_ENABLE_TELEMETRY")
	}
}

func TestBuildClaudeLaunchEnv_NoCollectorSkipsOTelInjection(t *testing.T) {
	clearOtelEnv(t)
	env := buildClaudeLaunchEnv("", nil)
	assertEnvEmptyOrUnset(t, env, "OTEL_EXPORTER_OTLP_ENDPOINT")
}

func TestBuildClaudeLaunchEnv_WithCollector(t *testing.T) {
	clearOtelEnv(t)
	env := buildClaudeLaunchEnv("", testOverrides(12345))
	assertEnvContains(t, env, "CLAUDE_CODE_ENABLE_TELEMETRY", "1")
	assertEnvContains(t, env, "OTEL_TRACES_EXPORTER", "otlp")
	assertEnvContains(t, env, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:12345")
	// OTel session ID goes to its own var; WIPNOTE_SESSION_ID must NOT carry it.
	assertEnvContains(t, env, "WIPNOTE_OTEL_SESSION_ID", "test-session")
	assertEnvEmptyOrUnset(t, env, "WIPNOTE_SESSION_ID")
}

func TestBuildClaudeLaunchEnv_InjectsWhenCollectorActive(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("WIPNOTE_OTEL_ENABLED", "1")

	env := buildClaudeLaunchEnv("", testOverrides(9999))
	assertEnvContains(t, env, "CLAUDE_CODE_ENABLE_TELEMETRY", "1")
	assertEnvContains(t, env, "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "1")
	assertEnvContains(t, env, "OTEL_METRICS_EXPORTER", "otlp")
	assertEnvContains(t, env, "OTEL_LOGS_EXPORTER", "otlp")
	assertEnvContains(t, env, "OTEL_TRACES_EXPORTER", "otlp")
	assertEnvContains(t, env, "OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	assertEnvContains(t, env, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:9999")
	assertEnvContains(t, env, "OTEL_LOG_TOOL_DETAILS", "1")
}

func TestBuildClaudeLaunchEnv_RespectsUserOverrides(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("WIPNOTE_OTEL_ENABLED", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://custom.example.com:4318")
	t.Setenv("OTEL_METRICS_EXPORTER", "console")
	t.Setenv("OTEL_LOG_TOOL_DETAILS", "0")

	env := buildClaudeLaunchEnv("", testOverrides(7777))
	assertEnvContains(t, env, "OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:7777")
	assertEnvContains(t, env, "OTEL_METRICS_EXPORTER", "console")
	assertEnvContains(t, env, "OTEL_LOG_TOOL_DETAILS", "0")
	assertEnvContains(t, env, "CLAUDE_CODE_ENABLE_TELEMETRY", "1")
}

func TestBuildClaudeLaunchEnv_WorktreeProjectDir(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("WIPNOTE_PROJECT_DIR", "/old/value")
	env := buildClaudeLaunchEnv("/worktree/main/.wipnote", nil)
	assertEnvContains(t, env, "WIPNOTE_PROJECT_DIR", "/worktree/main/.wipnote")
}

func TestIsTruthy(t *testing.T) {
	for _, s := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !isTruthy(s) {
			t.Errorf("isTruthy(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "0", "false", "no", "off", "maybe"} {
		if isTruthy(s) {
			t.Errorf("isTruthy(%q) = true, want false", s)
		}
	}
}

func TestIsExplicitlyDisabled(t *testing.T) {
	for _, s := range []string{"0", "false", "FALSE", "no", "off"} {
		if !isExplicitlyDisabled(s) {
			t.Errorf("isExplicitlyDisabled(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "1", "true", "yes", "random"} {
		if isExplicitlyDisabled(s) {
			t.Errorf("isExplicitlyDisabled(%q) = true, want false", s)
		}
	}
	for _, s := range []string{" 0", "false ", "  no  ", "\toff\t"} {
		if !isExplicitlyDisabled(s) {
			t.Errorf("isExplicitlyDisabled(%q) = false, want true (whitespace)", s)
		}
	}
}

// TestBuildClaudeLaunchEnv_InjectsAgentTeamsByDefault verifies that
// CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 is set in the returned env
// even when no OTel collector is active.
func TestBuildClaudeLaunchEnv_InjectsAgentTeamsByDefault(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "")

	env := buildClaudeLaunchEnv("", nil)
	assertEnvContains(t, env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "1")
}

// TestBuildClaudeLaunchEnv_UserOverrideOfAgentTeamsWins verifies that a
// user-set value for CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS is not overwritten
// by the registry default (addIfUnset semantics).
func TestBuildClaudeLaunchEnv_UserOverrideOfAgentTeamsWins(t *testing.T) {
	clearOtelEnv(t)
	t.Setenv("CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "0")

	env := buildClaudeLaunchEnv("", nil)
	assertEnvContains(t, env, "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "0")
}
