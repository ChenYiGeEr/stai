// Package git wraps the git porcelain stai needs. stai always invokes the
// user's own git, so it behaves identically inside SourceTree, other GUIs,
// and the plain shell.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RepoRoot returns the top-level directory of the current repository.
func RepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("not inside a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

// StagedDiff returns the diff of the index against HEAD for the current
// repository. It errors when nothing is staged.
func StagedDiff() ([]byte, error) {
	return StagedDiffDir("")
}

// StagedDiffDir is StagedDiff for the repository at dir ("" = the current
// directory). SourceTree custom actions pass the repo path as $REPO, so gen
// may run from anywhere.
func StagedDiffDir(dir string) ([]byte, error) {
	args := []string{"diff", "--cached", "--no-ext-diff", "--no-color"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git diff: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git diff: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.New("no staged changes — stage files with git add first")
	}
	return out, nil
}

// HookPath resolves where a hook script belongs for this repository
// (worktree- and submodule-safe via `git rev-parse --git-path`).
func HookPath(name string) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-path", "hooks/"+name).Output()
	if err != nil {
		return "", fmt.Errorf("resolving hook path: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
