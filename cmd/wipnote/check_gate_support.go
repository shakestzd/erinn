package main

import (
	"context"
	"database/sql"
	"io"
	"os"

	"github.com/shakestzd/wipnote/core/gateledger"
	"github.com/shakestzd/wipnote/internal/gate"
)

type gatePlan = gate.Plan
type gateAllowlistEntry = gate.AllowlistEntry
type gateAllowlistHit = gate.AllowlistHit
type gateRunResult = gate.RunResult
type gateCommand = gate.Command

func resolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon string) string {
	return gate.ResolveCodeRoot(projectRoot, cwdTop, cwdCommon, projCommon)
}

func gateCodeRoot(projectRoot string) string {
	return gate.CodeRoot(projectRoot)
}

func detectGatePlan(projectRoot, codeRoot, phase string) (gatePlan, error) {
	return gate.DetectPlan(projectRoot, codeRoot, phase)
}

func nodeGateCommands(manifestPath string) ([]gateCommand, error) {
	return gate.NodeGateCommands(manifestPath)
}

func runSessionGate(projectRoot, sessionID, workItemID, source, phase string, stdout, stderr io.Writer) (*gateRunResult, error) {
	return gate.RunSession(gate.RunOptions{
		ProjectRoot: projectRoot,
		SessionID:   sessionID,
		WorkItemID:  workItemID,
		Source:      source,
		Phase:       phase,
		Harness:     currentHarness(),
		Stdout:      stdout,
		Stderr:      stderr,
	})
}

func writeGateAllowlistHits(w io.Writer, hits []gateAllowlistHit) {
	gate.WriteAllowlistHits(w, hits)
}

func gateCommandAllowlisted(cmdErr error, hits []gateAllowlistHit) bool {
	return gate.GateCommandAllowlisted(cmdErr, hits)
}

func persistGateRecord(projectRoot, sessionID, workItemID, source string, result *gateRunResult) (*gateledger.Record, error) {
	return gate.PersistRecord(projectRoot, sessionID, workItemID, source, currentHarness(), result)
}

func loadGateAllowlist(projectRoot string) ([]gateAllowlistEntry, error) {
	return gate.LoadAllowlist(projectRoot)
}

func matchGateAllowlist(commandName, output string, entries []gateAllowlistEntry) []gateAllowlistHit {
	return gate.MatchAllowlist(commandName, output, entries)
}

func resolveGateWorkItem(projectRoot, sessionID, agentID, flagValue string, w io.Writer) string {
	return gate.ResolveWorkItem(projectRoot, sessionID, agentID, flagValue, w)
}

func reportGuardProfileDrift(database *sql.DB, projectRoot, sessionID string, w io.Writer) {
	gate.ReportGuardProfileDrift(database, projectRoot, sessionID, w)
}

// validateCompletionGateRecord takes no *sql.DB: the completion gate resolves its
// evidence from the canonical ledger, so its verdict is independent of index
// state (feat-0e5ca43e).
func validateCompletionGateRecord(projectRoot, sessionID, workItemID string) error {
	return gate.ValidateCompletionRecord(projectRoot, sessionID, workItemID, currentHarness(), os.Stdout, os.Stderr)
}

func gateSignalContext() (context.Context, context.CancelFunc) {
	return gate.SignalContext()
}
