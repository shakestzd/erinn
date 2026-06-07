package workitem

import "fmt"

// ErrNotFound returns a formatted error when a work item ID doesn't resolve.
// Two-line: what went wrong + how to fix.
func ErrNotFound(kind, id string) error {
	return fmt.Errorf("work item %q not found\nIDs look like feat-xxxxxxxx, bug-xxxxxxxx, spk-xxxxxxxx, trk-xxxxxxxx, plan-xxxxxxxx, spec-xxxxxxxx. Run 'wipnote %s list' to see valid IDs.", id, kind)
}

// ErrNotFoundOnDisk returns a formatted error when the HTML file is missing.
func ErrNotFoundOnDisk(kind, id string) error {
	return fmt.Errorf("work item %q not found on disk — the ID resolved but the HTML file is missing\nTry 'wipnote reindex' to rebuild the index, or 'wipnote %s list' to confirm.", id, kind)
}

// ErrNoActive returns a formatted error when no active items exist.
func ErrNoActive(kind, recoveryCmd string) error {
	return fmt.Errorf("no active %s found\nRun '%s' to get started.", kind, recoveryCmd)
}

// ErrUnknownValue returns a formatted error listing valid options.
func ErrUnknownValue(field, value string, valid []string) error {
	return fmt.Errorf("unknown %s %q\nValid options: %v", field, value, valid)
}
