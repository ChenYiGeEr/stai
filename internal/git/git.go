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
// may run from anywhere. An empty staging area is NOT an error: callers
// receive an empty diff with a nil error and must check len(diff) themselves
// (they surface a localized hint and skip the model call).
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
	return out, nil
}

// BranchDiffDir returns the diff of the current branch against baseRef
// (e.g. "origin/main") for the repository at dir ("" = current directory).
// It uses git's --merge-base form so the comparison is against the merge base
// of baseRef and HEAD, matching what a PR would show.
func BranchDiffDir(dir, baseRef string) ([]byte, error) {
	if baseRef == "" {
		baseRef = "origin/main"
	}
	args := []string{"diff", "--merge-base", baseRef, "HEAD", "--no-ext-diff", "--no-color"}
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
	return out, nil
}

// WorkingTreeDiffDir returns the diff of the working tree against HEAD for
// the repository at dir ("" = current directory). This includes both staged
// and unstaged changes, matching what `git stash` would capture. A clean
// working tree is NOT an error: callers receive an empty diff with a nil
// error and must check len(diff) themselves.
func WorkingTreeDiffDir(dir string) ([]byte, error) {
	args := []string{"diff", "HEAD", "--no-ext-diff", "--no-color"}
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
	return out, nil
}

// runGit runs git with args, capturing stderr (and any partial stdout) so
// failures carry git's own diagnostics instead of a bare exit status.
func runGit(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	stderrMsg := strings.TrimSpace(stderr.String())
	stdoutMsg := strings.TrimSpace(string(out))
	switch {
	case stderrMsg != "" && stdoutMsg != "":
		return nil, fmt.Errorf("%w: %s | stdout: %s", err, stderrMsg, stdoutMsg)
	case stderrMsg != "":
		return nil, fmt.Errorf("%w: %s", err, stderrMsg)
	case stdoutMsg != "":
		return nil, fmt.Errorf("%w: stdout: %s", err, stdoutMsg)
	}
	return nil, err
}

// LastTag returns the most recent git tag in the current repository.
func LastTag() (string, error) {
	return LastTagDir("")
}

// LastTagDir is LastTag for the repository at dir ("" = current directory).
func LastTagDir(dir string) (string, error) {
	args := []string{"describe", "--tags", "--abbrev=0"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := runGit(args...)
	if err != nil {
		return "", fmt.Errorf("git describe --tags --abbrev=0: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// LogRange returns the oneline log for the range since..until (e.g.
// "v1.0..HEAD") in the current repository. An empty since is allowed and
// returns the log for until only — callers that want a bounded range must
// pass an explicit since (e.g. the last tag) to avoid scanning full history
// on large repositories.
func LogRange(since, until string) ([]byte, error) {
	return LogRangeDir(since, until, "")
}

// LogRangeDir is LogRange for the repository at dir ("" = current directory).
func LogRangeDir(since, until, dir string) ([]byte, error) {
	if until == "" {
		return nil, errors.New("until ref must not be empty")
	}
	ref := until
	if since != "" {
		ref = since + ".." + until
	}
	args := []string{"log", ref, "--oneline", "--no-decorate", "--no-color"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := runGit(args...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
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
