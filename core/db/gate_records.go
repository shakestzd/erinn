package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/gateledger"
)

// GateRecord is a quality-gate run as it appears in the READ INDEX.
//
// The canonical record lives in core/gateledger (.wipnote/gate-ledger.html); this
// is the derived projection reindex rebuilds from it (feat-0e5ca43e, closing
// bug-550c1cd8). Rows here are queryable but never authoritative — the completion
// gate reads the ledger, not this table.
type GateRecord struct {
	ID int64
	// RecordID is the canonical ledger record id (gr-…) this row projects, or ""
	// for a legacy row written before the ledger existed. It is the key the
	// reindex projection is idempotent on.
	RecordID          string
	SessionID         string
	WorkItemID        string
	Harness           string
	ProjectType       string
	GateCommand       string
	Status            string
	CheckedAt         time.Time
	Signature         string
	AllowlistHitsJSON string
	AllowlistHitCount int
	Source            string
	OutputSummary     string
	// ProfileSignature is the canonical guardprofile.Signature of the APPROVED
	// guard profile that supplied this gate's commands, or "" when the gate ran
	// via manifest autodetection. It is provenance, NOT part of the
	// record-integrity signablePayload/Signature MAC.
	ProfileSignature string
	// GuardsRunJSON is a JSON array of the guard names that ran. "[]" when none
	// (autodetection or no-op).
	GuardsRunJSON string
}

// signatureInput builds the canonical signable field set.
//
// The algorithm deliberately lives in core/gateledger and is only reached from
// here: the SAME signature is computed over the canonical ledger record and
// verified against this projection, so two independent implementations would be
// a drift bug that only surfaces as a completion mysteriously refused.
func (gr *GateRecord) signatureInput() gateledger.SignatureInput {
	return gateledger.SignatureInput{
		SessionID:         gr.SessionID,
		WorkItemID:        gr.WorkItemID,
		Harness:           gr.Harness,
		ProjectType:       gr.ProjectType,
		GateCommand:       gr.GateCommand,
		Status:            gr.Status,
		CheckedAt:         gr.CheckedAt,
		AllowlistHitsJSON: gr.AllowlistHitsJSON,
		Source:            gr.Source,
		OutputSummary:     gr.OutputSummary,
	}
}

func (gr *GateRecord) ComputeSignature() string { return gr.signatureInput().Sum() }

func (gr *GateRecord) EnsureSignature() {
	gr.Signature = gr.ComputeSignature()
}

func (gr *GateRecord) SignatureValid() bool {
	if gr == nil || strings.TrimSpace(gr.Signature) == "" {
		return false
	}
	return gr.Signature == gr.ComputeSignature()
}

// InsertGateRecord projects one gate run into the read index, ignoring a record
// already projected. See InsertGateRecordIfAbsent for the inserted/ignored
// distinction.
func InsertGateRecord(database *sql.DB, gr *GateRecord) error {
	_, err := InsertGateRecordIfAbsent(database, gr)
	return err
}

// InsertGateRecordIfAbsent projects one gate run into the read index and reports
// whether a row was actually written.
//
// The insert is OR IGNORE against the partial unique index on record_id, so
// projecting the same canonical record twice — a gate run that wrote this row
// directly, followed by the reindex pass replaying the same ledger row — is a
// no-op rather than a duplicate. Legacy rows (record_id "") sit outside that
// index and keep the old insert-always behaviour.
func InsertGateRecordIfAbsent(database *sql.DB, gr *GateRecord) (bool, error) {
	if database == nil {
		return false, nil
	}
	if gr == nil {
		return false, fmt.Errorf("gate record is nil")
	}
	if gr.CheckedAt.IsZero() {
		gr.CheckedAt = time.Now().UTC()
	}
	if gr.AllowlistHitsJSON == "" {
		gr.AllowlistHitsJSON = "[]"
	}
	if gr.GuardsRunJSON == "" {
		gr.GuardsRunJSON = "[]"
	}
	if gr.Signature == "" {
		gr.EnsureSignature()
	}
	res, err := database.Exec(`
		INSERT OR IGNORE INTO gate_records (
			record_id, session_id, work_item_id, harness, project_type, gate_command,
			status, checked_at, signature, allowlist_hits_json,
			allowlist_hit_count, source, output_summary,
			profile_signature, guards_run
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gr.RecordID, gr.SessionID, nullStr(gr.WorkItemID), nullStr(gr.Harness), gr.ProjectType,
		gr.GateCommand, gr.Status, gr.CheckedAt.UTC().Format(time.RFC3339Nano),
		gr.Signature, gr.AllowlistHitsJSON, gr.AllowlistHitCount, gr.Source,
		nullStr(gr.OutputSummary), gr.ProfileSignature, gr.GuardsRunJSON,
	)
	if err != nil {
		return false, fmt.Errorf("insert gate record: %w", err)
	}
	// RowsAffected is the ONLY reliable inserted/ignored signal here: an ignored
	// INSERT OR IGNORE still reports the previous row's LastInsertId, so the id
	// alone cannot tell the two apart.
	affected, err := res.RowsAffected()
	if err != nil || affected == 0 {
		return false, nil
	}
	if id, err := res.LastInsertId(); err == nil {
		gr.ID = id
	}
	return true, nil
}

// GateRecordFromLedger projects a canonical ledger record into its index row.
func GateRecordFromLedger(r gateledger.Record) *GateRecord {
	return &GateRecord{
		RecordID:          r.ID,
		SessionID:         r.SessionID,
		WorkItemID:        r.WorkItemID,
		Harness:           r.Harness,
		ProjectType:       r.ProjectType,
		GateCommand:       r.GateCommand,
		Status:            r.Status,
		CheckedAt:         r.CheckedAt,
		Signature:         r.Signature,
		AllowlistHitsJSON: r.AllowlistHitsJSON,
		AllowlistHitCount: r.AllowlistHitCount,
		Source:            r.Source,
		OutputSummary:     r.OutputSummary,
		ProfileSignature:  r.ProfileSignature,
		GuardsRunJSON:     r.GuardsRunJSON,
	}
}

// UnledgeredGateRecords returns index rows that carry no canonical record id —
// gate runs written before the ledger existed.
//
// This is the input to the one-shot backfill that gives those runs a canonical
// home. Without it the seventy-five records bug-550c1cd8 counted would stay
// cache-only forever: the ledger would protect every FUTURE run and lose every
// past one on the first purge.
func UnledgeredGateRecords(database *sql.DB) ([]*GateRecord, error) {
	if database == nil {
		return nil, nil
	}
	rows, err := database.Query(`
		SELECT id, COALESCE(record_id,''), session_id, COALESCE(work_item_id,''), COALESCE(harness,''),
		       COALESCE(project_type,''), COALESCE(gate_command,''), COALESCE(status,''),
		       checked_at, COALESCE(signature,''), COALESCE(allowlist_hits_json,'[]'),
		       COALESCE(allowlist_hit_count,0), COALESCE(source,''), COALESCE(output_summary,''),
		       COALESCE(profile_signature,''), COALESCE(guards_run,'[]')
		FROM gate_records
		WHERE COALESCE(record_id,'') = ''
		ORDER BY checked_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query unledgered gate records: %w", err)
	}
	defer rows.Close()

	var out []*GateRecord
	for rows.Next() {
		rec, scanErr := scanGateRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if rec != nil {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

// SetGateRecordID stamps a legacy row with the canonical record id it was
// backfilled into, so the next backfill pass skips it.
func SetGateRecordID(database *sql.DB, rowID int64, recordID string) error {
	if database == nil || strings.TrimSpace(recordID) == "" {
		return nil
	}
	if _, err := database.Exec(`UPDATE gate_records SET record_id = ? WHERE id = ?`, recordID, rowID); err != nil {
		return fmt.Errorf("stamp gate record %d with %s: %w", rowID, recordID, err)
	}
	return nil
}

func LatestGateRecordForSession(database *sql.DB, sessionID string) (*GateRecord, error) {
	if database == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	row := database.QueryRow(`
		SELECT id, COALESCE(record_id,''), session_id, COALESCE(work_item_id,''), COALESCE(harness,''),
		       COALESCE(project_type,''), COALESCE(gate_command,''), COALESCE(status,''),
		       checked_at, COALESCE(signature,''), COALESCE(allowlist_hits_json,'[]'),
		       COALESCE(allowlist_hit_count,0), COALESCE(source,''), COALESCE(output_summary,''),
		       COALESCE(profile_signature,''), COALESCE(guards_run,'[]')
		FROM gate_records
		WHERE session_id = ?
		ORDER BY checked_at DESC, id DESC
		LIMIT 1`, sessionID)
	return scanGateRecord(row)
}

// LatestPassingGateRecordForWorkItem returns the most recent passing gate
// record for workItemID, regardless of which session produced it, provided the
// record was checked within the `within` window. When headCommit is non-empty
// the lookup additionally requires that the record's gate_command-independent
// output_summary or the caller's verification ties it to the current HEAD; HEAD
// matching is enforced by the caller (validateCompletionGateRecord) against the
// returned record's metadata, so this query only applies the work-item +
// status + recency filter. A nil record (no error) means no qualifying record
// exists. This is the cross-session fallback for bug-35857288: a work item
// validated by a passing gate in one session must be completable from another
// session at the same HEAD, instead of being rejected for lacking a
// session-scoped record.
func LatestPassingGateRecordForWorkItem(database *sql.DB, workItemID string, within time.Duration) (*GateRecord, error) {
	if database == nil || strings.TrimSpace(workItemID) == "" {
		return nil, nil
	}
	row := database.QueryRow(`
		SELECT id, COALESCE(record_id,''), session_id, COALESCE(work_item_id,''), COALESCE(harness,''),
		       COALESCE(project_type,''), COALESCE(gate_command,''), COALESCE(status,''),
		       checked_at, COALESCE(signature,''), COALESCE(allowlist_hits_json,'[]'),
		       COALESCE(allowlist_hit_count,0), COALESCE(source,''), COALESCE(output_summary,''),
		       COALESCE(profile_signature,''), COALESCE(guards_run,'[]')
		FROM gate_records
		WHERE work_item_id = ? AND status = 'pass'
		ORDER BY checked_at DESC, id DESC
		LIMIT 1`, workItemID)
	record, err := scanGateRecord(row)
	if err != nil || record == nil {
		return record, err
	}
	if within > 0 {
		if time.Since(record.CheckedAt) > within {
			return nil, nil
		}
	}
	return record, nil
}

// MostRecentInProgressWorkItem returns the ID of the most recently started
// in-progress work item (feature, bug, or spike) for the project, ordered by
// updated_at DESC then created_at DESC. Returns "" when no in-progress item
// exists or the database is nil. Used as last-resort attribution when the gate
// subprocess cannot resolve an active session-scoped claim.
func MostRecentInProgressWorkItem(database *sql.DB) string {
	if database == nil {
		return ""
	}
	var id string
	row := database.QueryRow(`
		SELECT id FROM features
		WHERE status = 'in-progress'
		ORDER BY updated_at DESC, created_at DESC
		LIMIT 1`)
	if err := row.Scan(&id); err != nil {
		return ""
	}
	return id
}

// GateRecordCountsForWorkItem returns the number of pass and fail gate runs
// recorded for workItemID, across every session that ever ran one. It is the
// outcome-evidence source `compliance auto` (feat-f9118b9c) joins against the
// spec/diff comparison, so a compliance verdict can reflect whether the
// item's own quality gates ever failed, not only whether the diff matches
// the spec.
//
// The two counts are complete by construction — every gate_records row for
// this item is counted, not sampled — so (0, 0) is a genuine "no gate runs
// recorded" rather than a coverage gap. Callers must still not read (0, 0)
// as "passed": it means no evidence, and feat-f9118b9c requires that
// absence never be promoted to a passing signal.
func GateRecordCountsForWorkItem(database *sql.DB, workItemID string) (pass, fail int, err error) {
	if database == nil || strings.TrimSpace(workItemID) == "" {
		return 0, 0, nil
	}
	rows, err := database.Query(
		`SELECT status, COUNT(*) FROM gate_records WHERE work_item_id = ? GROUP BY status`,
		workItemID)
	if err != nil {
		return 0, 0, fmt.Errorf("count gate records for %s: %w", workItemID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, fmt.Errorf("scan gate record count: %w", err)
		}
		switch status {
		case "pass":
			pass = count
		case "fail":
			fail = count
		}
	}
	return pass, fail, rows.Err()
}

func CountGateRecords(database *sql.DB, sessionID string) (int, error) {
	if database == nil {
		return 0, nil
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM gate_records WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count gate records: %w", err)
	}
	return count, nil
}

func scanGateRecord(scanner interface{ Scan(dest ...any) error }) (*GateRecord, error) {
	var gr GateRecord
	var checkedAt string
	err := scanner.Scan(
		&gr.ID, &gr.RecordID, &gr.SessionID, &gr.WorkItemID, &gr.Harness, &gr.ProjectType,
		&gr.GateCommand, &gr.Status, &checkedAt, &gr.Signature,
		&gr.AllowlistHitsJSON, &gr.AllowlistHitCount, &gr.Source, &gr.OutputSummary,
		&gr.ProfileSignature, &gr.GuardsRunJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gr.CheckedAt, _ = time.Parse(time.RFC3339Nano, checkedAt)
	if gr.CheckedAt.IsZero() {
		gr.CheckedAt, _ = time.Parse(time.RFC3339, checkedAt)
	}
	return &gr, nil
}
