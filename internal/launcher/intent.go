package launcher

import "strings"

type LaunchIntentKind string

const (
	LaunchIntentNew      LaunchIntentKind = "new"
	LaunchIntentContinue LaunchIntentKind = "continue"
)

// LaunchIntent is a harness-neutral contract describing whether a launcher
// should start fresh work or continue an existing work item/session.
type LaunchIntent struct {
	Kind            LaunchIntentKind
	Explicit        bool
	WorkItemID      string
	SessionHarness  string
	ResumeSessionID string
	WorktreePath    string
}

func NewWorkIntent() LaunchIntent {
	return LaunchIntent{Kind: LaunchIntentNew}
}

func ContinueWorkIntent(workItemID, sessionHarness, resumeSessionID, worktreePath string, explicit bool) LaunchIntent {
	return LaunchIntent{
		Kind:            LaunchIntentContinue,
		Explicit:        explicit,
		WorkItemID:      workItemID,
		SessionHarness:  sessionHarness,
		ResumeSessionID: resumeSessionID,
		WorktreePath:    worktreePath,
	}
}

func (i LaunchIntent) WantsContinue() bool {
	return i.Kind == LaunchIntentContinue
}

func (i LaunchIntent) ResumeForHarness(harness string) string {
	if !i.WantsContinue() {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(i.SessionHarness), strings.TrimSpace(harness)) {
		return ""
	}
	return i.ResumeSessionID
}
