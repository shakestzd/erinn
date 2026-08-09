package main

import (
	"os"

	"github.com/shakestzd/wipnote/core/agent"
)

// resolveSessionFamilyID returns the session family ID to use for a new
// launcher invocation. The rules are:
//
//  1. If WIPNOTE_SESSION_FAMILY_ID is already set in the environment (e.g. the
//     user is re-running inside a shell that already has the launcher env
//     injected), reuse it — this keeps all sub-invocations in one family.
//  2. If this is a resume/continue launch:
//     a. When the concrete resumed session ID is known (resumeID != ""), look
//     up THAT session's family directly. Resuming session X must join
//     family X — never an arbitrary parallel root's family.
//     b. When no concrete ID is known ("resume last"), inherit the family of
//     the most-recently-registered session by timestamp order, not by Go
//     map iteration (which is randomized and, with parallel roots, would
//     attach the resumed session to an arbitrary unrelated family).
//  3. Otherwise create a new family ID equal to the new session ID (each fresh
//     start is its own family of one until a resume joins it).
//
// projectDir and isResume let the caller signal resume intent. resumeID is the
// concrete harness session ID being resumed when the caller knows it (""
// otherwise — e.g. Antigravity's --continue or --conversation <id>).
// newSessionID is the freshly-minted OTel session ID for this launch.
func resolveSessionFamilyID(projectDir, newSessionID, resumeID string, isResume bool) string {
	// 1. Inherit from environment (nested / re-launched within the same family).
	if v := os.Getenv("WIPNOTE_SESSION_FAMILY_ID"); v != "" {
		return v
	}

	if isResume && projectDir != "" {
		// 2a. Concrete resumed session ID known → resolve its OWN family.
		if resumeID != "" {
			if fam := agent.SessionFamilyFor(projectDir, resumeID); fam != "" {
				return fam
			}
		}
		// 2b. "Resume last" / unknown concrete ID → most-recently-registered
		//     session's family by timestamp order (deterministic, not map order).
		if fam := agent.MostRecentSessionFamily(projectDir); fam != "" {
			return fam
		}
	}

	// 3. Fresh launch: new session = new family (self-as-family).
	return newSessionID
}

// persistLauncherSessionFamily writes the session→family mapping to the
// project's family index and writes the per-session state file. This is the
// CONCRETE write path that survives even if the SessionStart hook never fires
// (e.g. harness spawned without hooks configured).
//
// agentID is "codex" or "antigravity" (the harness name).
// Errors are silently ignored — this is a best-effort durability write;
// hook handlers are the authoritative path for DB writes.
func persistLauncherSessionFamily(projectDir, sessionID, agentID, familyID string) {
	if projectDir == "" || sessionID == "" {
		return
	}
	_ = agent.RegisterSessionFamily(projectDir, sessionID, familyID)
	_ = agent.WriteSessionState(projectDir, sessionID, agentID, familyID)
}
