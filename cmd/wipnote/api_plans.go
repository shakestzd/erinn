package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/plan/planamend"
	"github.com/shakestzd/wipnote/plan/planchat"
	"github.com/shakestzd/wipnote/plan/plantmpl"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// planListItem is a single entry in the GET /api/plans response.
type planListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	FeatureID string    `json:"feature_id"`
	Approved  int       `json:"approved"`
	Total     int       `json:"total"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// plansListHandler returns a JSON array of all plans sorted by mtime desc.
// GET /api/plans
func plansListHandler(wipnoteDir string, database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		plansDir := filepath.Join(wipnoteDir, "plans")
		entries, err := os.ReadDir(plansDir)
		if err != nil {
			if os.IsNotExist(err) {
				respondJSON(w, []planListItem{})
				return
			}
			http.Error(w, fmt.Sprintf("reading plans dir: %v", err), http.StatusInternalServerError)
			return
		}

		var items []planListItem
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
				continue
			}
			planID := strings.TrimSuffix(entry.Name(), ".html")
			planPath := filepath.Join(plansDir, entry.Name())

			item, err := parsePlanListItem(planPath, planID, database)
			if err != nil {
				continue
			}
			items = append(items, item)
		}

		sort.Slice(items, func(i, j int) bool {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})

		if items == nil {
			items = []planListItem{}
		}
		respondJSON(w, items)
	}
}

// parsePlanListItem reads a plan HTML file and extracts list metadata.
// Approval counts come from canonical YAML feedback/native slice state.
func parsePlanListItem(planPath, planID string, database *sql.DB) (planListItem, error) {
	info, err := os.Stat(planPath)
	if err != nil {
		return planListItem{}, err
	}

	f, err := os.Open(planPath)
	if err != nil {
		return planListItem{}, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return planListItem{}, err
	}

	article := doc.Find("article[id]").First()
	featureID, _ := article.Attr("data-feature-id")

	// Read status and slice metadata from YAML source of truth; fall back to
	// HTML attribute for backward compatibility (missing YAML, broken envs).
	status := ""
	version := 0
	var yamlSliceCount int
	var yamlPlan *planyaml.PlanYAML
	yamlPath := strings.TrimSuffix(planPath, ".html") + ".yaml"
	if loaded, yamlErr := planyaml.Load(yamlPath); yamlErr == nil {
		yamlPlan = loaded
		status = yamlPlan.Meta.Status
		version = yamlPlan.Meta.Version
		yamlSliceCount = len(yamlPlan.Slices)
	}
	if status == "" {
		// Fallback: parse HTML attribute (legacy — YAML is the canonical source).
		status, _ = article.Attr("data-status")
	}
	if status == "" {
		status = "draft"
	}

	title := strings.TrimSpace(doc.Find("h1").First().Text())
	if title == "" {
		title = planID
	}

	// Prefer YAML slice count as Total; fall back to HTML approve-input count
	// only for legacy plans that pre-date the slice YAML schema (zero slices).
	var total int
	if yamlSliceCount > 0 {
		total = yamlSliceCount
	} else {
		// Legacy fallback: count rendered approve checkboxes in HTML.
		doc.Find("input[data-action='approve']").Each(func(_ int, s *goquery.Selection) {
			total++
		})
	}

	approved := 0
	if yamlPlan != nil && yamlSliceCount > 0 {
		approved = countApprovedCanonicalSlices(yamlPlan.Slices)
	} else if yamlPlan != nil {
		approved = countApprovedFeedbackEntries(feedbackEntriesFromPlan(planID, yamlPlan))
	}

	// For legacy plans with no YAML: fall back to HTML checked attrs if SQLite empty.
	if approved == 0 && yamlSliceCount == 0 {
		doc.Find("input[data-action='approve']").Each(func(_ int, s *goquery.Selection) {
			if _, exists := s.Attr("checked"); exists {
				approved++
			}
		})
	}

	return planListItem{
		ID:        planID,
		Title:     title,
		Status:    status,
		FeatureID: featureID,
		Approved:  approved,
		Total:     total,
		Version:   version,
		UpdatedAt: info.ModTime().UTC(),
	}, nil
}

// countApprovedSlices counts the number of canonical YAML-approved slices.
func countApprovedSlices(_ *sql.DB, _ string, slices []planyaml.PlanSlice) int {
	return countApprovedCanonicalSlices(slices)
}

func countApprovedCanonicalSlices(slices []planyaml.PlanSlice) int {
	count := 0
	for _, s := range slices {
		if s.ApprovalStatus == "approved" || s.Approved {
			count++
		}
	}
	return count
}

func countApprovedFeedbackEntries(entries []dbpkg.PlanFeedback) int {
	count := 0
	for _, fb := range entries {
		if fb.Action == "approve" && dbpkg.IsPlanApprovalValueApproved(fb.Value) {
			count++
		}
	}
	return count
}

// toPlanSliceApprovals converts a []planyaml.PlanSlice to []dbpkg.PlanSliceApproval,
// the minimal type that core/db accepts without importing plan/planyaml.
func toPlanSliceApprovals(slices []planyaml.PlanSlice) []dbpkg.PlanSliceApproval {
	if len(slices) == 0 {
		return nil
	}
	out := make([]dbpkg.PlanSliceApproval, len(slices))
	for i, s := range slices {
		out[i] = dbpkg.PlanSliceApproval{
			Num:            s.Num,
			ApprovalStatus: s.ApprovalStatus,
			Approved:       s.Approved,
		}
	}
	return out
}

// planStatusResponse is returned by GET /api/plans/{id}/status.
type planStatusResponse struct {
	PlanID        string `json:"plan_id"`
	Status        string `json:"status"`
	ApprovedCount int    `json:"approved_count"`
	TotalSections int    `json:"total_sections"`
}

// planFeedbackResponse is returned by GET /api/plans/{id}/feedback.
type planFeedbackResponse struct {
	PlanID       string                     `json:"plan_id"`
	Status       string                     `json:"status"`
	Sections     map[string]sectionFeedback `json:"sections"`
	Questions    map[string]string          `json:"questions"`
	Annotations  []planAnnotationEntry      `json:"annotations,omitempty"`
	ChatMessages []chatMessageEntry         `json:"chat_messages,omitempty"`
}

// planAnnotationEntry is a single block-anchored annotation in the feedback
// response (slice-8). It surfaces the two-axis state so a reviewer's UI (and
// the read-feedback-yaml path) can render which notes have been consumed by an
// agent vs resolved, and where each is routed.
type planAnnotationEntry struct {
	Section          string `json:"section"`
	Anchor           string `json:"anchor"`
	Comment          string `json:"comment"`
	QuestionID       string `json:"question_id,omitempty"`
	Consumed         bool   `json:"consumed"`
	Resolved         bool   `json:"resolved"`
	ResolutionTarget string `json:"resolution_target,omitempty"`
}

type sectionFeedback struct {
	Approved bool   `json:"approved"`
	Value    string `json:"value,omitempty"`
	Comment  string `json:"comment"`
}

// chatMessageEntry is a single chat message in the feedback response.
type chatMessageEntry struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// planFeedbackRequest is the body for POST /api/plans/{id}/feedback.
//
// When Action == "annotation" (slice-8 block-level annotations), the Anchor,
// Consumed, Resolved and ResolutionTarget fields carry the two-axis state.
// They are ignored for all other actions, so legacy approve/comment/answer
// requests are unaffected.
type planFeedbackRequest struct {
	Section          string `json:"section"`
	Action           string `json:"action"`
	Value            string `json:"value"`
	QuestionID       string `json:"question_id"`
	Anchor           string `json:"anchor,omitempty"`
	Consumed         bool   `json:"consumed,omitempty"`
	Resolved         bool   `json:"resolved,omitempty"`
	ResolutionTarget string `json:"resolution_target,omitempty"`
}

// planFileHandler serves HTML plan files from .wipnote/plans/{id}.html.
// GET /plans/{id}.html
func planFileHandler(wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// URL path: /plans/{id}.html — strip the /plans/ prefix.
		name := strings.TrimPrefix(r.URL.Path, "/plans/")
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
			http.Error(w, "invalid plan path", http.StatusBadRequest)
			return
		}
		if !strings.HasSuffix(name, ".html") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		planPath := filepath.Join(wipnoteDir, "plans", name)
		if _, err := os.Stat(planPath); err != nil {
			http.Error(w, "plan not found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, planPath)
	}
}

// planStatusHandler returns status information for a plan.
// GET /api/plans/{id}/status
func planStatusHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/status")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		planPath, err := resolvePlanPath(wipnoteDir, planID)
		if err != nil {
			http.Error(w, "plan not found", http.StatusNotFound)
			return
		}

		htmlStatus, err := parsePlanHTMLStatus(planPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("reading plan: %v", err), http.StatusInternalServerError)
			return
		}

		approvedCount, totalSections, err := countPlanSections(wipnoteDir, planID)
		if err != nil {
			http.Error(w, fmt.Sprintf("querying feedback: %v", err), http.StatusInternalServerError)
			return
		}

		respondJSON(w, planStatusResponse{
			PlanID:        planID,
			Status:        htmlStatus,
			ApprovedCount: approvedCount,
			TotalSections: totalSections,
		})
	}
}

// validSectionRe matches valid plan feedback section keys used by:
//
//	design                          — design approvals
//	outline                         — outline approvals
//	meta                            — plan metadata actions (e.g. finalize flag)
//	critique                        — critique section approvals
//	chat                            — chat session messages
//	q-<name>                        — question answers (legacy)
//	slice-<num>                     — slice-level approval (slice-4)
//	slice-<num>-question-<id>       — slice-local question answer (slice-4)
//	slice-<num>-block-<name>-<idx>  — block-anchored annotation (slice-8)
//
// The block-anchor alternative is TIGHTLY BOUNDED: it requires the literal
// "slice-N-block-" prefix, a lowercase-kebab block name, and a trailing numeric
// index. It deliberately does NOT open the section key to an arbitrary string —
// the existing approval/answer contract must stay exact so finalize-yaml and the
// slice-approval gates keep matching only the keys they expect.
var validSectionRe = regexp.MustCompile(`^(design|outline|meta|critique|chat|slice-\d+-block-[a-z0-9-]+-\d+|slice-\d+-question-[a-z0-9-]+|slice-\d+|q-[a-z0-9-]+)$`)

// planFeedbackSubmitHandler stores a feedback entry for a plan section.
// POST /api/plans/{id}/feedback
func planFeedbackSubmitHandler(_ *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req planFeedbackRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}
		if req.Section == "" || req.Action == "" {
			http.Error(w, "section and action are required", http.StatusBadRequest)
			return
		}

		// Normalize underscores to hyphens for slice sections — a common
		// mistake that used to silently store wrong-format keys that
		// finalize-yaml couldn't find.
		// Handles both 'slice_N' and 'slice_N_question_<id>' patterns (slice-4).
		if rest, ok := strings.CutPrefix(req.Section, "slice_"); ok {
			req.Section = "slice-" + strings.ReplaceAll(rest, "_", "-")
		}

		if !validSectionRe.MatchString(req.Section) {
			http.Error(w, fmt.Sprintf("invalid section %q — must match: design, outline, meta, critique, chat, slice-N, slice-N-question-<id>, or q-<name>", req.Section), http.StatusBadRequest)
			return
		}

		planID, err := extractPlanID(r.URL.Path, "/feedback")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Block-level annotations (slice-8) carry the anchor + two-axis state
		// (consumed/resolved/resolution_target) and are persisted via the
		// dedicated StorePlanAnnotation upsert. All other actions
		// (approve/comment/answer/finalize) use the original feedback path.
		if req.Action == "annotation" {
			if err := storePlanAnnotation(wipnoteDir, planID, req); err != nil {
				http.Error(w, fmt.Sprintf("storing annotation: %v", err), http.StatusInternalServerError)
				return
			}
			respondJSON(w, map[string]string{"status": "ok"})
			return
		}

		if err := storePlanFeedback(wipnoteDir, planID, req.Section, req.Action, req.Value, req.QuestionID); err != nil {
			http.Error(w, fmt.Sprintf("storing feedback: %v", err), http.StatusInternalServerError)
			return
		}

		respondJSON(w, map[string]string{"status": "ok"})
	}
}

// planFinalizeHandler finalizes a plan once all sections are approved.
// POST /api/plans/{id}/finalize
func planFinalizeHandler(_ *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/finalize")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		approved, pending, err := canonicalPlanApproved(wipnoteDir, planID)
		if err != nil {
			http.Error(w, fmt.Sprintf("checking approval: %v", err), http.StatusInternalServerError)
			return
		}
		if !approved {
			message := "not all sections approved"
			if len(pending) > 0 {
				message = fmt.Sprintf("not all slices approved (pending: %v)", pending)
			}
			http.Error(w, message, http.StatusBadRequest)
			return
		}

		feedback, err := readPlanFeedbackEntries(wipnoteDir, planID)
		if err != nil {
			http.Error(w, fmt.Sprintf("reading feedback: %v", err), http.StatusInternalServerError)
			return
		}

		// Create track and features from approved slices, mirroring what the CLI
		// does via `wipnote plan finalize-yaml`. Partial failures are logged and
		// reported in the response — a finalized plan with N/M features created is
		// better than aborting and leaving the plan in a half-finalized state.
		//
		createdFeatures, featFailures, featErr := finalizeYAMLCanonical(wipnoteDir, planID)
		if featErr != nil {
			log.Printf("warning: finalizeYAMLCanonical failed for %s: %v", planID, featErr)
		}
		for _, f := range featFailures {
			log.Printf("warning: plan %s slice %d (%s): feature creation failed: %s", planID, f.SliceNum, f.Title, f.Error)
		}
		if createdFeatures == nil {
			createdFeatures = []string{}
		}

		type failureInfo struct {
			SliceNum int    `json:"slice_num"`
			Title    string `json:"title"`
			Error    string `json:"error"`
		}
		var failureInfos []failureInfo
		for _, f := range featFailures {
			failureInfos = append(failureInfos, failureInfo{SliceNum: f.SliceNum, Title: f.Title, Error: f.Error})
		}
		if failureInfos == nil {
			failureInfos = []failureInfo{}
		}

		// Read track ID from YAML (written by finalizeYAMLCanonical) for the
		// next-step commands surfaced in the dashboard finalize result panel.
		trackID := ""
		yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
		if finalizedPlan, yErr := planyaml.Load(yamlPath); yErr == nil {
			trackID = finalizedPlan.Meta.TrackID
		}
		nextCmd, yoloCmd := planNextCommands(planID, trackID)

		respondJSON(w, map[string]any{
			"plan_id":          planID,
			"status":           "finalized",
			"feedback":         feedback,
			"created_features": createdFeatures,
			"failures":         failureInfos,
			"track_id":         trackID,
			"next_command":     nextCmd,
			"yolo_command":     yoloCmd,
		})
	}
}

// planFeedbackReadHandler returns structured feedback for a plan.
// GET /api/plans/{id}/feedback
func planFeedbackReadHandler(_ *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID, err := extractPlanID(r.URL.Path, "/feedback")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
		if err != nil {
			http.Error(w, fmt.Sprintf("reading feedback: %v", err), http.StatusInternalServerError)
			return
		}

		respondJSON(w, buildFeedbackResponse(planID, entries))
	}
}

// planFeedbackHandler routes GET and POST for /api/plans/{id}/feedback.
// bug-74a7bda7: POST (StorePlanFeedback) uses the writable handle; GET
// (GetPlanFeedback) stays on the read-only handle.
func planFeedbackHandler(database, writeDB *sql.DB, wipnoteDir string) http.HandlerFunc {
	submitH := planFeedbackSubmitHandler(writeDB, wipnoteDir)
	readH := planFeedbackReadHandler(database, wipnoteDir)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			submitH(w, r)
		case http.MethodGet:
			readH(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// planDeleteHandler deletes a draft plan's HTML file and feedback.
// DELETE /api/plans/{id}/delete
func planDeleteHandler(_ *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/delete")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		planPath, err := resolvePlanPath(wipnoteDir, planID)
		if err != nil {
			http.Error(w, "plan not found", http.StatusNotFound)
			return
		}

		htmlStatus, err := parsePlanHTMLStatus(planPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("reading plan: %v", err), http.StatusInternalServerError)
			return
		}

		// Only allow deletion of draft or in-progress plans
		if htmlStatus == "finalized" {
			http.Error(w, "Cannot delete a finalized plan", http.StatusBadRequest)
			return
		}

		// Delete the HTML file
		if err := os.Remove(planPath); err != nil {
			http.Error(w, fmt.Sprintf("deleting plan file: %v", err), http.StatusInternalServerError)
			return
		}

		respondJSON(w, map[string]string{"status": "deleted", "plan_id": planID})
	}
}

// planChatRequest is the body for POST /api/plans/{id}/chat.
type planChatRequest struct {
	Message string `json:"message"`
}

// planChatHandler streams Claude responses as SSE for a plan chat session.
// POST /api/plans/{id}/chat
func planChatHandler(_ *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		planID, err := extractPlanID(r.URL.Path, "/chat")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req planChatRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "reading request body", http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}
		if req.Message == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}

		// Load plan YAML for context.
		planContext := loadPlanContext(wipnoteDir, planID)

		// Resolve project dir (parent of .wipnote/).
		projectDir := filepath.Dir(wipnoteDir)

		backend := planchat.NewWithSession(planID, planContext, projectDir, planChatSessionID(wipnoteDir, planID))
		if !backend.IsAvailable() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Claude unavailable. Install claude CLI or set ANTHROPIC_API_KEY.",
			})
			return
		}

		// Store user message.
		_ = appendPlanChatMessage(wipnoteDir, planID, "user", req.Message)

		// Set SSE headers.
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Stream response.
		chunks, errCh := backend.Send(r.Context(), req.Message)

		var fullResponse strings.Builder
		for chunk := range chunks {
			fullResponse.WriteString(chunk)
			payload, _ := json.Marshal(map[string]string{
				"type": "chunk",
				"text": chunk,
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}

		// Check for errors.
		if err := <-errCh; err != nil {
			payload, _ := json.Marshal(map[string]string{
				"type":  "error",
				"error": err.Error(),
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}

		// Store assistant message.
		if fullResponse.Len() > 0 {
			_ = appendPlanChatMessage(wipnoteDir, planID, "assistant", fullResponse.String())
			if sessionID := backend.SessionID(); sessionID != "" {
				_ = storePlanFeedback(wipnoteDir, planID, "chat", "session_id", sessionID, "")
			}

			// Detect and store AMEND directives from the assistant response.
			amendments := planamend.ParseAmendments(fullResponse.String())
			for _, a := range amendments {
				section := fmt.Sprintf("slice-%d", a.SliceNum)
				value, _ := json.Marshal(a)
				if err := storePlanFeedback(wipnoteDir, planID, section, "amendment", string(value), ""); err != nil {
					log.Printf("warning: store amendment for plan %s slice %d: %v", planID, a.SliceNum, err)
				}
			}
		}

		// Send done event.
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"done"}`)
		flusher.Flush()
	}
}

// loadPlanContext reads the plan YAML file for use as Claude context.
// Falls back to empty string if the file is not found.
func loadPlanContext(wipnoteDir, planID string) string {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		// Try HTML fallback for plan context.
		htmlPath := filepath.Join(wipnoteDir, "plans", planID+".html")
		data, err = os.ReadFile(htmlPath)
		if err != nil {
			return ""
		}
	}
	return string(data)
}

// planRouter dispatches /api/plans/{id}/{action} to the appropriate handler.
// Registered under /api/plans/ in serve.go.
// planRouter fans /api/plans/{id}/* out to per-action sub-handlers.
// bug-74a7bda7 (roborev HIGH follow-up): read-only sub-handlers
// (status, render, events, amendments, yaml, feedback GET) run on the
// read-only mux handle `database`; mutating sub-handlers
// (feedback POST, finalize, delete, chat) run on the dedicated writable
// handle `writeDB` so they don't fail with SQLITE_READONLY.
func planRouter(database, writeDB *sql.DB, wipnoteDir string) http.HandlerFunc {
	statusH := planStatusHandler(database, wipnoteDir)
	feedbackH := planFeedbackHandler(database, writeDB, wipnoteDir)
	finalizeH := planFinalizeHandler(writeDB, wipnoteDir)
	deleteH := planDeleteHandler(writeDB, wipnoteDir)
	chatH := planChatHandler(writeDB, wipnoteDir)
	amendmentsH := planAmendmentsHandler(wipnoteDir)
	yamlH := planYAMLHandler(wipnoteDir)
	renderH := planRenderHandler(database, wipnoteDir)
	eventsH := planEventsHandler(wipnoteDir)
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/chat"):
			chatH(w, r)
		case strings.HasSuffix(path, "/render"):
			renderH(w, r)
		case strings.HasSuffix(path, "/events"):
			eventsH(w, r)
		case strings.HasSuffix(path, "/status"):
			statusH(w, r)
		case strings.HasSuffix(path, "/feedback"):
			feedbackH(w, r)
		case strings.HasSuffix(path, "/finalize"):
			finalizeH(w, r)
		case strings.HasSuffix(path, "/delete"):
			deleteH(w, r)
		case strings.HasSuffix(path, "/amendments"):
			amendmentsH(w, r)
		case strings.HasSuffix(path, "/yaml"):
			yamlH(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

// planEmbedScope is the CSS selector of the SPA container that the rendered
// plan is injected into (#plan-detail-body, which also carries this class).
// The plan's full stylesheet is re-scoped under this selector by scopePlanCSS
// so it applies to the embedded plan only and matches the standalone page.
const planEmbedScope = ".plan-detail-body"

// planRenderHandler dynamically renders plan HTML from the YAML source.
// Returns just the plan content (no outer HTML shell/sidebar) for embedding
// in the dashboard detail panel.
// GET /api/plans/{id}/render
func planRenderHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/render")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Build a PlanPage dynamically from the YAML source so the content
		// is always up-to-date — the static HTML may be stale or empty.
		page := plantmpl.BuildFromTopic(planID, "", "", "")
		enrichPageFromYAML(wipnoteDir, planID, page)
		enrichRelatedWork(database, page)

		// If YAML enrichment didn't populate the title, fall back to
		// extracting it from the static HTML file.
		if page.Title == "" {
			htmlPath := filepath.Join(wipnoteDir, "plans", planID+".html")
			if data, err := os.ReadFile(htmlPath); err == nil {
				if doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data))); err == nil {
					page.Title = doc.Find("h1").First().Text()
				}
			}
		}
		if page.Title == "" {
			page.Title = planID
		}

		// Render the full page, then extract styles + article + scripts
		// so the embedded view has complete CSS and interactivity.
		var buf strings.Builder
		if err := page.Render(&buf); err != nil {
			http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(buf.String()))
		if err != nil {
			http.Error(w, "parse error", http.StatusInternalServerError)
			return
		}

		var out strings.Builder

		// Emit the plan's FULL stylesheet, SCOPED under the embed container so
		// it renders at full fidelity (same CSS as the standalone page) without
		// leaking into — or being overridden by — the dashboard shell. No rules
		// are stripped; selectors are re-anchored under planEmbedScope. This is
		// what makes the SPA panel visually match /plans/<id>.html: the
		// :root custom properties, slice-card controls, status accent borders,
		// pills, and left-nav triage badges all resolve correctly inside the
		// container instead of being dropped.
		//
		// CRITICAL: Use .Text() not .Html() — .Html() HTML-entity-escapes the
		// CSS text, turning " into &#34;, which breaks selectors like
		// input[type="radio"]. .Text() returns the raw text content, correct for
		// <style> raw-text content elements.
		doc.Find("style").Each(func(_ int, s *goquery.Selection) {
			css := s.Text()
			if css == "" {
				return
			}
			out.WriteString("<style>")
			out.WriteString(scopePlanCSS(css, planEmbedScope))
			out.WriteString("</style>\n")
		})

		// Include CDN link tags (highlight.js, fonts)
		doc.Find("link[rel='stylesheet'], link[rel='preconnect']").Each(func(_ int, s *goquery.Selection) {
			outerHTML, _ := goquery.OuterHtml(s)
			out.WriteString(outerHTML)
			out.WriteString("\n")
		})

		// Emit CONTENT ONLY (no .plan-sidebar chrome). The dashboard owns all
		// chrome: brand header, primary nav, plan sub-nav with per-slice triage
		// badges. Emitting the plan's own .plan-sidebar here would produce a
		// second redundant nav column inside the panel. The scoped CSS still
		// includes .plan-sidebar rules (they are not stripped) but the sidebar
		// element itself is intentionally omitted.
		//
		// The #graph-data [data-node] bridge divs live inside .dep-graph inside
		// .plan-layout and are therefore included — the dashboard JS reads them
		// after injection to build its per-slice slice-nav with triage badges.
		body := doc.Find("body")
		layout, _ := goquery.OuterHtml(body.Find(".plan-layout").First())
		if layout == "" {
			// Fallback: emit entire body content if the layout wrapper is absent.
			layout, _ = body.Html()
		}
		out.WriteString(layout)

		// Include scripts (D3, dagre-d3, plan JS)
		doc.Find("script").Each(func(_ int, s *goquery.Selection) {
			outerHTML, _ := goquery.OuterHtml(s)
			out.WriteString(outerHTML)
			out.WriteString("\n")
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, out.String())
	}
}

// enrichRelatedWork populates the plan page's related track and features
// by looking up their titles and statuses from the features table.
func enrichRelatedWork(database *sql.DB, page *plantmpl.PlanPage) {
	if database == nil {
		return
	}

	// Look up the linked track
	if page.FeatureID != "" {
		var title, status string
		err := database.QueryRow(
			`SELECT COALESCE(title, id), COALESCE(status, 'todo') FROM features WHERE id = ?`,
			page.FeatureID,
		).Scan(&title, &status)
		if err == nil && title != "" {
			page.RelatedTrack = &plantmpl.RelatedWorkItem{
				ID:     page.FeatureID,
				Title:  title,
				Type:   "track",
				Status: status,
			}
		}
	}

	// Look up slice features
	for _, sc := range page.Slices {
		if sc.ID == "" {
			continue
		}
		var title, status string
		err := database.QueryRow(
			`SELECT COALESCE(title, id), COALESCE(status, 'todo') FROM features WHERE id = ?`,
			sc.ID,
		).Scan(&title, &status)
		if err != nil {
			title = sc.Title
			status = "todo"
		}
		page.RelatedFeatures = append(page.RelatedFeatures, plantmpl.RelatedWorkItem{
			ID:     sc.ID,
			Title:  title,
			Type:   "feature",
			Status: status,
		})
	}
}

// planEventsHandler streams plan feedback changes as SSE.
// GET /api/plans/{id}/events
func planEventsHandler(wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/events")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		seen := make(map[string]time.Time)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
				if err != nil {
					continue
				}
				for _, e := range entries {
					key := planEventKey(e)
					updated := e.UpdatedAt
					if updated.IsZero() {
						updated = e.CreatedAt
					}
					if last, ok := seen[key]; ok && !updated.After(last) {
						continue
					}
					payload, _ := json.Marshal(map[string]string{
						"plan_id": planID, "section": e.Section,
						"action": e.Action, "value": e.Value,
					})
					fmt.Fprintf(w, "data: %s\n\n", payload)
					seen[key] = updated
				}
				flusher.Flush()
			}
		}
	}
}

func planEventKey(e dbpkg.PlanFeedback) string {
	return e.Section + "\x00" + e.Action + "\x00" + e.QuestionID + "\x00" + e.Anchor
}

// planYAMLHandler serves the raw YAML source for a plan.
// GET /api/plans/{id}/yaml
func planYAMLHandler(wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/yaml")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := os.ReadFile(filepath.Join(wipnoteDir, "plans", planID+".yaml"))
		if err != nil {
			http.Error(w, "plan YAML not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(data)
	}
}

// planAmendmentsHandler returns amendments parsed from plan feedback entries.
// GET /api/plans/{id}/amendments
func planAmendmentsHandler(wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		planID, err := extractPlanID(r.URL.Path, "/amendments")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
		if err != nil {
			http.Error(w, fmt.Sprintf("reading feedback: %v", err), http.StatusInternalServerError)
			return
		}

		type amendmentEntry struct {
			planamend.Amendment
			Status string `json:"status"` // pending, accepted, rejected
		}

		// Build a map from question_id -> status for amendment_status entries.
		statusMap := make(map[string]string)
		for _, e := range entries {
			if e.Action == "amendment_status" && e.QuestionID != "" {
				statusMap[e.QuestionID] = e.Value
			}
		}

		var amendments []amendmentEntry
		for _, e := range entries {
			if e.Action != "amendment" {
				continue
			}
			var a planamend.Amendment
			if err := json.Unmarshal([]byte(e.Value), &a); err != nil {
				continue
			}
			status := "pending"
			if s, ok := statusMap[e.QuestionID]; ok {
				status = s
			}
			amendments = append(amendments, amendmentEntry{Amendment: a, Status: status})
		}

		if amendments == nil {
			amendments = []amendmentEntry{}
		}
		respondJSON(w, amendments)
	}
}

// ---- helpers ----------------------------------------------------------------

// planNextCommands returns the agentic dispatch commands for a finalized plan.
// Both planFinalizeHandler (API) and printFinalizeYAMLSummary (CLI) use this
// so the format string is defined in exactly one place.
func planNextCommands(planID, trackID string) (nextCommand, yoloCommand string) {
	nextCommand = fmt.Sprintf("/wipnote:execute %s", planID)
	yoloCommand = fmt.Sprintf("wipnote yolo --track %s", trackID)
	return
}

// extractPlanID parses a plan ID from URL paths of the form
// /api/plans/{id}/{suffix}. Returns an error if the ID is missing.
func extractPlanID(urlPath, suffix string) (string, error) {
	const prefix = "/api/plans/"
	path := strings.TrimSuffix(urlPath, "/")
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("unexpected path: %s", urlPath)
	}
	mid := path[len(prefix):]
	mid = strings.TrimSuffix(mid, suffix)
	if mid == "" || strings.Contains(mid, "/") {
		return "", fmt.Errorf("missing or invalid plan ID in path: %s", urlPath)
	}
	return mid, nil
}

// resolvePlanPath returns the absolute path to a plan's HTML file, or an
// error if the file does not exist.
func resolvePlanPath(wipnoteDir, planID string) (string, error) {
	p := filepath.Join(wipnoteDir, "plans", planID+".html")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("plan %s not found", planID)
	}
	return p, nil
}

// parsePlanHTMLStatus reads the plan's YAML source of truth and returns
// meta.status. The planPath argument is the HTML path; YAML is derived via
// TrimSuffix so callers do not need to change their invocations.
func parsePlanHTMLStatus(planPath string) (string, error) {
	yamlPath := strings.TrimSuffix(planPath, ".html") + ".yaml"
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return "", fmt.Errorf("load plan YAML for status: %w", err)
	}
	status := plan.Meta.Status
	if status == "" {
		status = "draft"
	}
	return status, nil
}

// finalizePlanHTML writes a snapshot of the finalized plan with all feedback
// baked into the HTML: checkboxes checked, radio buttons selected, comments
// filled, and data-status set to "finalized". The HTML file becomes a
// self-contained record of the finalized plan.
func finalizePlanHTML(planPath string, feedback []dbpkg.PlanFeedback) error {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}

	// Set article status to finalized
	doc.Find("article").First().SetAttr("data-status", "finalized")

	for _, fb := range feedback {
		switch fb.Action {
		case "approve":
			// Handle approval inputs. For slice-card YAML plans, these are radio buttons
			// with three values (approved, changes_requested, rejected). For legacy plans,
			// these are checkboxes. Must branch on type to preserve radio-group invariant.
			section := fb.Section
			approved := dbpkg.IsPlanApprovalValueApproved(fb.Value)

			// For radios: set checked only on value='approved' if approved, clear all otherwise
			doc.Find(fmt.Sprintf("input[type='radio'][data-section='%s'][data-action='approve']", section)).
				Each(func(_ int, s *goquery.Selection) {
					val, _ := s.Attr("value")
					if approved && val == "approved" {
						s.SetAttr("checked", "checked")
					} else {
						s.RemoveAttr("checked")
					}
				})

			// For checkboxes: set checked only if approved
			if approved {
				doc.Find(fmt.Sprintf("input[type='checkbox'][data-section='%s'][data-action='approve']", section)).
					SetAttr("checked", "checked")
			} else {
				doc.Find(fmt.Sprintf("input[type='checkbox'][data-section='%s'][data-action='approve']", section)).
					RemoveAttr("checked")
			}
		case "comment":
			// Set textarea content for this section
			doc.Find(fmt.Sprintf("textarea[data-section='%s']", fb.Section)).
				SetText(fb.Value)
		case "answer":
			// Select the radio button matching this answer
			doc.Find(fmt.Sprintf("input[type='radio'][data-question='%s']", fb.QuestionID)).
				Each(func(_ int, s *goquery.Selection) {
					val, _ := s.Attr("value")
					if val == fb.Value {
						s.SetAttr("checked", "checked")
					} else {
						s.RemoveAttr("checked")
					}
				})
		}
	}

	html, err := doc.Html()
	if err != nil {
		return err
	}
	return os.WriteFile(planPath, []byte(html), 0o644)
}

// countPlanSections returns the count of approved UI-exposed approval sections
// and the total UI-exposed approval sections with feedback for the given plan.
func countPlanSections(wipnoteDir, planID string) (approved, total int, err error) {
	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		return 0, 0, err
	}
	sections := make(map[string]bool)
	approvedSections := make(map[string]bool)
	for _, e := range entries {
		if e.Action != "approve" || !dbpkg.IsPlanApprovalSection(e.Section) {
			continue
		}
		sections[e.Section] = true
		if dbpkg.IsPlanApprovalValueApproved(e.Value) {
			approvedSections[e.Section] = true
		} else {
			delete(approvedSections, e.Section)
		}
	}
	return len(approvedSections), len(sections), nil
}

// buildFeedbackResponse groups raw feedback entries into the structured
// response consumed by the CLI and other API callers.
func buildFeedbackResponse(planID string, entries []dbpkg.PlanFeedback) planFeedbackResponse {
	sections := make(map[string]sectionFeedback)
	questions := make(map[string]string)
	approvedSections := make(map[string]bool)
	var chatMessages []chatMessageEntry
	var annotations []planAnnotationEntry

	for _, e := range entries {
		switch e.Action {
		case "annotation":
			annotations = append(annotations, planAnnotationEntry{
				Section:          e.Section,
				Anchor:           e.Anchor,
				Comment:          e.Value,
				QuestionID:       e.QuestionID,
				Consumed:         e.Consumed,
				Resolved:         e.Resolved,
				ResolutionTarget: e.ResolutionTarget,
			})
		case "approve":
			sf := sections[e.Section]
			sf.Approved = dbpkg.IsPlanApprovalValueApproved(e.Value)
			sf.Value = approvalStatusFromValue(e.Value)
			sections[e.Section] = sf
			if sf.Approved {
				approvedSections[e.Section] = true
			} else {
				delete(approvedSections, e.Section)
			}
		case "comment":
			sf := sections[e.Section]
			sf.Comment = e.Value
			sections[e.Section] = sf
		case "answer":
			if e.QuestionID != "" {
				questions[e.QuestionID] = e.Value
			}
		case "messages":
			// Chat messages stored as a JSON array under section='chat'.
			if e.Section == "chat" && e.Value != "" {
				var msgs []chatMessageEntry
				if json.Unmarshal([]byte(e.Value), &msgs) == nil {
					chatMessages = msgs
				}
			}
		}
	}

	// Exclude chat section from approval status calculation.
	delete(sections, "chat")
	delete(approvedSections, "chat")

	status := "draft"
	if len(sections) > 0 && len(approvedSections) == len(sections) {
		status = "approved"
	}

	return planFeedbackResponse{
		PlanID:       planID,
		Status:       status,
		Sections:     sections,
		Questions:    questions,
		Annotations:  annotations,
		ChatMessages: chatMessages,
	}
}
