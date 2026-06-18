package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// commitRecapArtifact stages and commits the recap HTML file at
// .wipnote/recaps/<recapID>.html to the main git repository that contains
// wipnoteDir.
//
// Design mirrors commitWipnoteArtifact: `git -C <repoRoot>` anchors to the
// main repository even when the caller's CWD is inside a linked worktree.
// The per-worktree .wipnote/ gitignore exclusion is bypassed by passing the
// explicit absolute path to `git add --`. Non-fatal: if git is unavailable or
// the commit fails for any reason the function logs to stderr and returns nil.
func commitRecapArtifact(wipnoteDir, recapID string) error {
	repoRoot := filepath.Dir(wipnoteDir)

	absWipnote, err := filepath.Abs(wipnoteDir)
	if err != nil {
		absWipnote = wipnoteDir
	}
	if isTestTmpPath(absWipnote) {
		if os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "recap autocommit skipped: test temp path: %s\n", absWipnote)
		}
		return nil
	}

	if !isGitRepo(repoRoot) {
		fmt.Fprintf(stderr, "recap autocommit skipped: %s is not inside a git repository\n", repoRoot)
		return nil
	}

	absPath := filepath.Join(wipnoteDir, "recaps", recapID+".html")
	msg := "wipnote: recap " + recapID

	var resultErr error
	_, lockErr := withGitMutationLock(repoRoot, func() ([]byte, error) {
		if addOut, addErr := runGitWithLockRetry(repoRoot, "add", "--", absPath); addErr != nil {
			resultErr = fmt.Errorf("recap autocommit: git add %s: %s: %w",
				recapID, strings.TrimSpace(string(addOut)), addErr)
			return nil, addErr
		}
		if exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet", "--", absPath).Run() == nil {
			return nil, nil
		}
		if commitOut, commitErr := runGitWithLockRetry(repoRoot, "commit", "-m", msg, "--", absPath); commitErr != nil {
			outStr := string(commitOut)
			if strings.Contains(outStr, "nothing to commit") || strings.Contains(outStr, "no changes added") {
				return nil, nil
			}
			fmt.Fprintf(stderr, "recap autocommit warning: git commit failed for %s: %s\n",
				recapID, strings.TrimSpace(outStr))
		}
		return nil, nil
	})
	if resultErr == nil {
		resultErr = lockErr
	}
	return resultErr
}
