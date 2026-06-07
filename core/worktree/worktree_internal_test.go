package worktree

import "io"

func init() {
	// Tests don't want to spawn the real `wipnote reindex` subprocess.
	reindexWorktreeFn = func(string, io.Writer) {}
}
