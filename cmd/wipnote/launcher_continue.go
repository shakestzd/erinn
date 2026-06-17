package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/launcher"
)

const (
	continueHandoffEnvVar = "WIPNOTE_CONTINUE_HANDOFF_B64"
	continuedFromEnvVar   = "WIPNOTE_CONTINUED_FROM"
)

type continueLaunchContext struct {
	WorkItemID          string
	WorktreePath        string
	ContinuedFrom       string
	TranscriptResumeID  string
	TranscriptResumeOK  bool
	HandoffMarkdown     string
	Warnings            []string
	transcriptSkipCause string
}

func (c continueLaunchContext) ExtraEnv() []string {
	var env []string
	if c.ContinuedFrom != "" {
		env = append(env, continuedFromEnvVar+"="+c.ContinuedFrom)
	}
	if c.HandoffMarkdown != "" {
		env = append(env, continueHandoffEnvVar+"="+base64.StdEncoding.EncodeToString([]byte(c.HandoffMarkdown)))
	}
	return env
}

func resolveContinueLaunchContext(projectRoot, canonicalRoot, harness string, intent launcher.LaunchIntent) (continueLaunchContext, error) {
	ctx := continueLaunchContext{
		WorkItemID: strings.TrimSpace(intent.WorkItemID),
	}
	if !intent.WantsContinue() || ctx.WorkItemID == "" {
		return ctx, nil
	}

	root := strings.TrimSpace(canonicalRoot)
	if root == "" {
		root = strings.TrimSpace(projectRoot)
	}
	if root == "" {
		return ctx, nil
	}

	db, err := openReadOnlyDB(filepath.Join(root, ".wipnote"))
	if err != nil {
		return ctx, err
	}
	defer db.Close()

	row, sess, err := loadContinueSessionContext(db, root, intent)
	if err != nil {
		return ctx, err
	}
	if row == nil {
		ctx.Warnings = append(ctx.Warnings,
			fmt.Sprintf("wipnote continue: no resumable session metadata found for %s; starting a fresh continuation.", ctx.WorkItemID))
		return ctx, nil
	}

	if ctx.WorkItemID == "" {
		ctx.WorkItemID = row.WorkItemID
	}
	ctx.ContinuedFrom = strings.TrimSpace(row.LastSessionID)
	if sess != nil && strings.TrimSpace(sess.SessionID) != "" {
		ctx.ContinuedFrom = strings.TrimSpace(sess.SessionID)
	}

	if wt, ok := resolveContinueWorktreePath(root, firstNonEmpty(
		intent.WorktreePath,
		sessionExecWorktree(sess),
		row.ExecWorktreePath,
	)); ok {
		ctx.WorktreePath = wt
	} else {
		ctx.Warnings = append(ctx.Warnings,
			fmt.Sprintf("wipnote continue: previous worktree for %s is missing or unavailable; starting a fresh rehydrated continuation.", ctx.WorkItemID))
	}

	prevHarness := normalizeContinueHarness(firstNonEmpty(intent.SessionHarness, row.Harness, sessionHarness(sess)))
	liveCollision, liveMsg := continueLiveCollision(db, root, ctx.WorkItemID)
	if liveCollision {
		ctx.Warnings = append(ctx.Warnings, liveMsg)
		ctx.transcriptSkipCause = "live claim conflict detected"
	}

	if !liveCollision {
		switch harness {
		case "claude", "codex":
			if prevHarness == harness && ctx.ContinuedFrom != "" {
				ctx.TranscriptResumeID = ctx.ContinuedFrom
				ctx.TranscriptResumeOK = true
			} else {
				ctx.transcriptSkipCause = continueTranscriptSkipCause(harness, prevHarness, ctx.ContinuedFrom)
			}
		case "gemini":
			ctx.transcriptSkipCause = "Gemini resume is still constrained to its CLI index/latest contract here, so chooser-driven continue stays fresh+rehydrated"
		case "antigravity":
			ctx.transcriptSkipCause = "Antigravity conversation IDs are not persisted as wipnote session IDs, so chooser-driven continue stays fresh+rehydrated"
		default:
			ctx.transcriptSkipCause = "no supported transcript resume path"
		}
	}

	if !ctx.TranscriptResumeOK && ctx.transcriptSkipCause != "" {
		ctx.Warnings = append(ctx.Warnings,
			fmt.Sprintf("wipnote continue: transcript resume skipped for %s; %s.", ctx.WorkItemID, ctx.transcriptSkipCause))
	}

	ctx.HandoffMarkdown = buildContinueHandoffMarkdown(ctx, prevHarness, sess)
	return ctx, nil
}

func loadContinueSessionContext(db *sql.DB, projectRoot string, intent launcher.LaunchIntent) (*dbpkg.ResumableSession, *models.Session, error) {
	rows, err := dbpkg.ListResumableSessions(db, dbpkg.LivenessStalenessThreshold(projectRoot))
	if err != nil {
		return nil, nil, err
	}
	for i := range rows {
		row := rows[i]
		if strings.TrimSpace(row.WorkItemID) != strings.TrimSpace(intent.WorkItemID) {
			continue
		}
		sid := strings.TrimSpace(intent.ResumeSessionID)
		if sid == "" {
			sid = strings.TrimSpace(row.LastSessionID)
		}
		var sess *models.Session
		if sid != "" {
			sess, err = dbpkg.GetSession(db, sid)
			if err != nil {
				return &row, nil, nil
			}
		}
		return &row, sess, nil
	}
	return nil, nil, nil
}

func continueLiveCollision(db *sql.DB, projectRoot, workItemID string) (bool, string) {
	if db == nil || strings.TrimSpace(workItemID) == "" {
		return false, ""
	}
	state, err := dbpkg.LiveCollision(db, workItemID, "", dbpkg.LivenessStalenessThreshold(projectRoot))
	if err != nil || !state.HasLiveCollision || len(state.LiveClaimants) == 0 {
		return false, ""
	}
	claimant := state.LiveClaimants[0]
	return true, fmt.Sprintf(
		"wipnote continue: %s is still live in session %s; falling back to a fresh rehydrated continuation instead of transcript resume",
		workItemID, claimant.OwnerSessionID,
	)
}

func resolveContinueWorktreePath(projectRoot, recorded string) (string, bool) {
	recorded = strings.TrimSpace(recorded)
	if recorded == "" {
		return "", false
	}
	path := recorded
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectRoot, filepath.FromSlash(recorded))
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return path, true
}

func continueTranscriptSkipCause(currentHarness, previousHarness, previousSessionID string) string {
	switch {
	case previousSessionID == "":
		return "no previous session ID was recorded"
	case previousHarness == "":
		return "the previous harness is unknown"
	case previousHarness != currentHarness:
		return fmt.Sprintf("the previous session ran under %s, not %s", previousHarness, currentHarness)
	default:
		return "no safe transcript resume path is available"
	}
}

func buildContinueHandoffMarkdown(ctx continueLaunchContext, previousHarness string, sess *models.Session) string {
	if ctx.WorkItemID == "" && sess == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Continued Session Handoff\n\n")
	fmt.Fprintf(&b, "Work item: `%s`\n", ctx.WorkItemID)
	if ctx.ContinuedFrom != "" {
		fmt.Fprintf(&b, "Previous wipnote session: `%s`\n", ctx.ContinuedFrom)
	}
	if previousHarness != "" {
		fmt.Fprintf(&b, "Previous harness: `%s`\n", previousHarness)
	}
	if ctx.WorktreePath != "" {
		fmt.Fprintf(&b, "Reuse worktree: `%s`\n", ctx.WorktreePath)
	}
	if ctx.TranscriptResumeOK {
		b.WriteString("Transcript resume: enabled in addition to this persisted handoff.\n")
	} else {
		fmt.Fprintf(&b, "Transcript resume: skipped; starting a fresh session with persisted handoff (%s).\n",
			firstNonEmpty(ctx.transcriptSkipCause, "fresh continuation requested"))
	}

	if sess == nil {
		return strings.TrimSpace(b.String())
	}
	if notes := strings.TrimSpace(sess.HandoffNotes); notes != "" {
		b.WriteString("\n### Handoff Notes\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}
	if next := strings.TrimSpace(sess.RecommendedNext); next != "" {
		b.WriteString("\n### Recommended Next\n")
		b.WriteString(next)
		b.WriteString("\n")
	}
	if blockers := decodeStringList(sess.Blockers); len(blockers) > 0 {
		b.WriteString("\n### Recent Blockers\n")
		for _, blocker := range blockers {
			fmt.Fprintf(&b, "- %s\n", blocker)
		}
	}
	b.WriteString("\nReconcile this handoff with the current repo state before making new changes.")
	return strings.TrimSpace(b.String())
}

func decodeStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	filtered := out[:0]
	for _, item := range out {
		item = strings.TrimSpace(item)
		if item != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func mergeLauncherEnv(env []string, extra ...string) []string {
	if len(extra) == 0 {
		return env
	}
	return launcher.AppendOrReplaceEnv(env, extra...)
}

func normalizeContinueHarness(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func sessionExecWorktree(sess *models.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ExecWorktreePath
}

func sessionHarness(sess *models.Session) string {
	if sess == nil {
		return ""
	}
	return sess.Harness
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
