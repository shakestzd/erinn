// Regression test for bug-528478ad: per-project mux plan mutation routes
// must route through a writable DB handle, not the read-only dashboard handle.
//
// Confirmed failure on v0.61.1: POST /api/plans/<id>/feedback → 500
// "storing feedback: ... attempt to write a readonly database (8)"
// because buildSingleProjectMux was called with `database` (read-only) for
// both arguments, so planFeedbackSubmitHandler, planFinalizeHandler,
// planDeleteHandler, and planChatHandler all received a query_only=ON handle.
//
// These tests exercise the mux directly via httptest — no serve child spawned.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/storage"
)

// minimalPlanHTML is the bare minimum HTML accepted by parsePlanHTMLStatus —
// the function reads the companion YAML, not the HTML itself, so the file
// just needs to exist for plan-list and route discovery.
const minimalPlanHTML = `<!DOCTYPE html><html><body><article data-status="draft"><h1>Test Plan</h1></article></body></html>`

// minimalPlanYAML is a draft plan YAML that parsePlanHTMLStatus loads to
// derive the plan status. The status field determines what operations are
// allowed (e.g. finalize requires draft/review, delete blocks finalized).
const minimalPlanYAML = `meta:
  id: %s
  title: Bug-528478ad Test Plan
  description: regression test plan
  created_at: "2026-01-01T00:00:00Z"
  status: draft
  version: 1
design:
  problem: Test problem
  goals:
    - test goal
  constraints: []
  approved: false
  comment: ""
slices: []
questions: []
`

// setupPlanTestProject creates a temporary project with a .wipnote/plans/
// directory, seeds a plan HTML+YAML pair, boots the DB schema, and returns
// (wipnoteDir, dbPath, planID). The caller is responsible for any cleanup
// beyond the TempDir lifecycle.
func setupPlanTestProject(t *testing.T, planID string) (wipnoteDir, dbPath string) {
	t.Helper()
	projectRoot := t.TempDir()
	wipnoteDir = filepath.Join(projectRoot, ".wipnote")
	plansDir := filepath.Join(wipnoteDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	// Seed plan HTML.
	htmlPath := filepath.Join(plansDir, planID+".html")
	if err := os.WriteFile(htmlPath, []byte(minimalPlanHTML), 0o644); err != nil {
		t.Fatalf("write plan html: %v", err)
	}

	// Seed plan YAML (required by parsePlanHTMLStatus).
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := os.WriteFile(yamlPath, []byte(fmt.Sprintf(minimalPlanYAML, planID)), 0o644); err != nil {
		t.Fatalf("write plan yaml: %v", err)
	}

	// Bootstrap schema so plan_feedback table exists.
	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		t.Fatalf("resolve db path: %v", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		t.Fatalf("ensure db dir: %v", err)
	}
	boot, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap schema: %v", err)
	}
	boot.Close()

	return wipnoteDir, dbPath
}

// TestPlanFeedbackPOST_WritableMux is the primary regression for bug-528478ad:
// POST /api/plans/<id>/feedback must persist a plan_feedback row when
// buildSingleProjectMux is called with a proper writable writeDB.
//
// Before the fix this returned 500 "attempt to write a readonly database (8)"
// because writeDB was the read-only `database` handle (query_only=ON).
func TestPlanFeedbackPOST_WritableMux(t *testing.T) {
	planID := "plan-bug528478ad-feedback"
	wipnoteDir, dbPath := setupPlanTestProject(t, planID)

	// Open the two handles exactly as runServeChild does post-fix.
	readDB, err := dbpkg.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readDB.Close()

	writeDB, err := dbpkg.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writeDB.Close()

	mux := buildSingleProjectMux(readDB, writeDB, wipnoteDir)

	// POST feedback — this is the exact request the dashboard Approve button sends.
	body, _ := json.Marshal(map[string]string{
		"section": "design",
		"action":  "approve",
		"value":   "1",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/plans/"+planID+"/feedback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/plans/%s/feedback: got %d %s, want 200",
			planID, rec.Code, rec.Body.String())
	}

	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		t.Fatalf("read canonical feedback: %v", err)
	}
	if len(entries) == 0 || entries[0].Section != "design" {
		t.Fatalf("canonical feedback not persisted after POST feedback: %+v", entries)
	}
}

// TestPlanFeedbackPOST_ReadOnlyMux_Returns500 is retained as a topology guard:
// plan feedback is now canonical YAML, so it must not depend on either DB
// handle being writable.
func TestPlanFeedbackPOST_ReadOnlyMux_Returns500(t *testing.T) {
	planID := "plan-bug528478ad-readonly"
	wipnoteDir, dbPath := setupPlanTestProject(t, planID)

	readDB, err := dbpkg.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readDB.Close()

	// Reproduce the pre-fix bug: pass readDB as BOTH database and writeDB.
	mux := buildSingleProjectMux(readDB, readDB, wipnoteDir)

	body, _ := json.Marshal(map[string]string{
		"section": "design",
		"action":  "approve",
		"value":   "1",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/api/plans/"+planID+"/feedback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST feedback with read-only DB handles got %d %s, want 200",
			rec.Code, rec.Body.String())
	}
	if _, err := readPlanFeedbackEntries(wipnoteDir, planID); err != nil {
		t.Fatalf("read canonical feedback: %v", err)
	}
}

// TestPlanFinalizePOST_WritableMux verifies that POST /finalize also works
// with a proper writable writeDB (finalize writes the meta.finalize feedback row
// and calls finalizePlanHTML which mutates the HTML file).
func TestPlanFinalizePOST_WritableMux(t *testing.T) {
	planID := "plan-bug528478ad-finalize"
	wipnoteDir, dbPath := setupPlanTestProject(t, planID)

	readDB, err := dbpkg.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer readDB.Close()

	writeDB, err := dbpkg.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writeDB.Close()

	mux := buildSingleProjectMux(readDB, writeDB, wipnoteDir)

	// bug-fddf5820 (finding 13): the prior version accepted a pre-write 400
	// ("not all sections approved") as a pass, so it never proved finalize
	// actually WRITES through the writable handle. Seed the approval the legacy
	// gate requires (the minimal plan has no slices, so IsPlanFullyApproved
	// needs the design section approved), then assert the finalize succeeds and
	// persists its write.
	if err := storePlanFeedback(wipnoteDir, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("seed design approval: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/api/plans/"+planID+"/finalize",
		nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/plans/%s/finalize: got %d %s, want 200 (approved plan must finalize through writable mux)",
			planID, rec.Code, rec.Body.String())
	}

	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		t.Fatalf("read canonical feedback: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("expected canonical feedback after successful finalize, got none")
	}
}
