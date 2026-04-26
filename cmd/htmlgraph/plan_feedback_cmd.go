package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

// planFeedbackApproval represents approval state and optional comment for one section.
type planFeedbackApproval struct {
	Approved bool   `json:"approved"`
	Comment  string `json:"comment,omitempty"`
}

// planFeedbackAmendment represents a single accepted amendment directive.
type planFeedbackAmendment struct {
	Field string `json:"field"`
	Slice int    `json:"slice,omitempty"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// planFeedbackChatMessage is a single chat message from the review session.
type planFeedbackChatMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// planFeedbackJSON is the complete feedback payload written to stdout.
type planFeedbackJSON struct {
	PlanID       string                          `json:"plan_id"`
	Approvals    map[string]planFeedbackApproval `json:"approvals"`
	Answers      map[string]string               `json:"answers"`
	Amendments   []planFeedbackAmendment         `json:"amendments"`
	ChatMessages []planFeedbackChatMessage       `json:"chat_messages"`
}

// planFeedbackCmd returns the cobra command for `htmlgraph plan feedback <plan-id>`.
func planFeedbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "feedback <plan-id>",
		Short: "Dump all plan feedback from SQLite as JSON",
		Long: `Read all feedback for a YAML plan from the SQLite database and write it
to stdout as JSON. Includes approvals (per slice), question answers, accepted
amendments, and chat messages. Useful for agents running without HTTP server.

Example:
  htmlgraph plan feedback plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanFeedback(args[0])
		},
	}
}

func runPlanFeedback(planID string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	return planFeedback(htmlgraphDir, planID)
}

// planFeedback is the testable inner implementation.
func planFeedback(htmlgraphDir, planID string) error {
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(htmlgraphDir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	db, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	out := planFeedbackJSON{
		PlanID:       planID,
		Approvals:    make(map[string]planFeedbackApproval),
		Answers:      make(map[string]string),
		Amendments:   []planFeedbackAmendment{},
		ChatMessages: []planFeedbackChatMessage{},
	}

	rows, err := db.Query(
		"SELECT section, action, value, question_id FROM plan_feedback WHERE plan_id = ? ORDER BY created_at ASC",
		planID,
	)
	if err != nil {
		return fmt.Errorf("query plan_feedback: %w", err)
	}
	defer rows.Close()

	// Collect raw comment strings to merge with approvals after the loop.
	comments := make(map[string]string)

	for rows.Next() {
		var section, action, value, qid string
		if err := rows.Scan(&section, &action, &value, &qid); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		switch action {
		case "approve":
			entry := out.Approvals[section]
			entry.Approved = strings.EqualFold(value, "true")
			out.Approvals[section] = entry

		case "comment":
			comments[section] = value

		case "answer":
			if qid != "" {
				out.Answers[qid] = value
			}

		case "accepted":
			// Amendment stored as JSON in the value column.
			if section == "amendment" {
				var raw struct {
					SliceNum  int    `json:"slice_num"`
					Field     string `json:"field"`
					Operation string `json:"operation"`
					Content   string `json:"content"`
				}
				if json.Unmarshal([]byte(value), &raw) == nil {
					out.Amendments = append(out.Amendments, planFeedbackAmendment{
						Field: raw.Field,
						Slice: raw.SliceNum,
						Op:    raw.Operation,
						Value: raw.Content,
					})
				}
			}

		case "messages":
			// Chat messages — section='chat', action='messages', value=JSON array.
			if section == "chat" {
				var msgs []planFeedbackChatMessage
				if json.Unmarshal([]byte(value), &msgs) == nil {
					out.ChatMessages = msgs
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	// Merge comments into approvals map.
	for section, comment := range comments {
		entry := out.Approvals[section]
		entry.Comment = comment
		out.Approvals[section] = entry
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
