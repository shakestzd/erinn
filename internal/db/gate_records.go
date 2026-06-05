package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GateRecord is a session-local derived quality-gate run stored in the read
// index. It is intentionally NOT canonical .wipnote state.
type GateRecord struct {
	ID                int64
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

func (gr *GateRecord) signablePayload() string {
	checkedAt := gr.CheckedAt.UTC().Format(time.RFC3339Nano)
	return strings.Join([]string{
		gr.SessionID,
		gr.WorkItemID,
		gr.Harness,
		gr.ProjectType,
		gr.GateCommand,
		gr.Status,
		checkedAt,
		gr.AllowlistHitsJSON,
		gr.Source,
		gr.OutputSummary,
	}, "\n")
}

func (gr *GateRecord) ComputeSignature() string {
	sum := sha256.Sum256([]byte(gr.signablePayload()))
	return fmt.Sprintf("%x", sum[:])
}

func (gr *GateRecord) EnsureSignature() {
	gr.Signature = gr.ComputeSignature()
}

func (gr *GateRecord) SignatureValid() bool {
	if gr == nil || strings.TrimSpace(gr.Signature) == "" {
		return false
	}
	return gr.Signature == gr.ComputeSignature()
}

func InsertGateRecord(database *sql.DB, gr *GateRecord) error {
	if database == nil {
		return nil
	}
	if gr == nil {
		return fmt.Errorf("gate record is nil")
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
		INSERT INTO gate_records (
			session_id, work_item_id, harness, project_type, gate_command,
			status, checked_at, signature, allowlist_hits_json,
			allowlist_hit_count, source, output_summary,
			profile_signature, guards_run
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gr.SessionID, nullStr(gr.WorkItemID), nullStr(gr.Harness), gr.ProjectType,
		gr.GateCommand, gr.Status, gr.CheckedAt.UTC().Format(time.RFC3339Nano),
		gr.Signature, gr.AllowlistHitsJSON, gr.AllowlistHitCount, gr.Source,
		nullStr(gr.OutputSummary), gr.ProfileSignature, gr.GuardsRunJSON,
	)
	if err != nil {
		return fmt.Errorf("insert gate record: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		gr.ID = id
	}
	return nil
}

func LatestGateRecordForSession(database *sql.DB, sessionID string) (*GateRecord, error) {
	if database == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	row := database.QueryRow(`
		SELECT id, session_id, COALESCE(work_item_id,''), COALESCE(harness,''),
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

func decodeGateAllowlistHits(raw string) []map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var hits []map[string]any
	_ = json.Unmarshal([]byte(raw), &hits)
	return hits
}

func scanGateRecord(scanner interface{ Scan(dest ...any) error }) (*GateRecord, error) {
	var gr GateRecord
	var checkedAt string
	err := scanner.Scan(
		&gr.ID, &gr.SessionID, &gr.WorkItemID, &gr.Harness, &gr.ProjectType,
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
