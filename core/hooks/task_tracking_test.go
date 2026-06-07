package hooks

// TestBuildStepDesc_* tests were removed: they re-implemented addTaskStep's
// string-building logic inline rather than calling the function, providing
// zero refactor protection. The same behavior is covered by
// TestTeammateIdle_RecordsTeammateName and the TaskCreated/TaskCompleted
// tests in missing_events_test.go.
