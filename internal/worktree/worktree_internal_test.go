package worktree

import "io"

func init() {
	// Tests don't want to spawn the real `htmlgraph reindex` subprocess.
	reindexWorktreeFn = func(string, io.Writer) {}
}
