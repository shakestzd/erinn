package main

import (
	"encoding/json"
	"fmt"
	"os"

	dbpkg "github.com/shakestzd/wipnote/core/db"
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

// planFeedbackCmd returns the cobra command for `wipnote plan feedback <plan-id>`.
func planFeedbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "feedback <plan-id>",
		Short: "Dump all canonical plan feedback as JSON",
		Long: `Read all feedback for a YAML plan from the canonical plan YAML and write it
to stdout as JSON. Includes approvals (per slice), question answers, accepted
amendments, and chat messages. Useful for agents running without HTTP server.

Example:
  wipnote plan feedback plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanFeedback(args[0])
		},
	}
}

func runPlanFeedback(planID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	return planFeedback(wipnoteDir, planID)
}

// planFeedback is the testable inner implementation.
func planFeedback(wipnoteDir, planID string) error {
	out := planFeedbackJSON{
		PlanID:       planID,
		Approvals:    make(map[string]planFeedbackApproval),
		Answers:      make(map[string]string),
		Amendments:   []planFeedbackAmendment{},
		ChatMessages: []planFeedbackChatMessage{},
	}

	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		return fmt.Errorf("read plan feedback: %w", err)
	}

	// Collect raw comment strings to merge with approvals after the loop.
	comments := make(map[string]string)

	for _, entry := range entries {
		switch entry.Action {
		case "approve":
			approval := out.Approvals[entry.Section]
			approval.Approved = dbpkg.IsPlanApprovalValueApproved(entry.Value)
			out.Approvals[entry.Section] = approval

		case "comment":
			comments[entry.Section] = entry.Value

		case "answer":
			if entry.QuestionID != "" {
				out.Answers[entry.QuestionID] = entry.Value
			}

		case "accepted":
			// Amendment stored as JSON in the value column.
			if entry.Section == "amendment" {
				var raw struct {
					SliceNum  int    `json:"slice_num"`
					Field     string `json:"field"`
					Operation string `json:"operation"`
					Content   string `json:"content"`
				}
				if json.Unmarshal([]byte(entry.Value), &raw) == nil {
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
			if entry.Section == "chat" {
				var msgs []planFeedbackChatMessage
				if json.Unmarshal([]byte(entry.Value), &msgs) == nil {
					out.ChatMessages = msgs
				}
			}
		}
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
