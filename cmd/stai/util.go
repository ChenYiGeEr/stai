package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"stai/internal/i18n"
)

// fatal prints the error to stderr and exits with code 1.
func fatal(err error) {
	if err != nil {
		logf("error: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		os.Exit(1)
	}
}

// utf8Cmd builds a command that runs with a UTF-8 locale. SourceTree starts
// custom actions without one: pbcopy then writes an empty pasteboard, and
// pbpaste and osascript mangle non-ASCII text, so stai's own read-back check
// would fail even when the write was fine.
func utf8Cmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	// Filter out existing locale vars so our UTF-8 settings take precedence.
	// LC_ALL overrides LANG/LC_CTYPE, so we must also clear it.
	env := os.Environ()
	filtered := make([]string, 0, len(env)+3)
	for _, e := range env {
		if strings.HasPrefix(e, "LANG=") ||
			strings.HasPrefix(e, "LC_CTYPE=") ||
			strings.HasPrefix(e, "LC_ALL=") {
			continue
		}
		filtered = append(filtered, e)
	}
	cmd.Env = append(filtered,
		"LANG=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)
	return cmd
}

// filepathEvalSymlinks resolves symlinks in a path.
func filepathEvalSymlinks(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}

// commandContext derives a timeout context from parent for a CLI command.
// Callers pass context.Background() today — stai is a short-lived CLI process
// with no upstream cancellation — but accepting parent keeps the door open
// for embedding stai commands in a larger cancellable flow.
func commandContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

// notImplemented prints a placeholder message for unimplemented commands.
func notImplemented(cmd, plan string) {
	fmt.Printf(i18n.T("mergetool_not_impl"), cmd, plan)
}

// cmdMergetool is a placeholder for the mergetool command (M3).
func cmdMergetool(args []string) {
	fs := flag.NewFlagSet("mergetool", flag.ExitOnError)
	fs.Parse(args)
	notImplemented("mergetool",
		"M3: wrap git mergetool, per-hunk explanation + recommendation, never auto-write")
}

// validateFileInRepo ensures filePath is inside repoDir, preventing a custom
// action from reading arbitrary files via a crafted $FILE. Both paths are
// resolved to their real (symlink-free) locations first, so a symlink inside
// the repo cannot point outside it; ".." traversal is rejected via
// filepath.Rel. On Windows, filepath.Rel across drives returns an absolute
// path, which we also reject.
func validateFileInRepo(filePath, repoDir string) error {
	absFile, err := filepathEvalSymlinks(filePath)
	if err != nil {
		return fmt.Errorf("resolving file path: %w", err)
	}
	absRepo, err := filepathEvalSymlinks(repoDir)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}
	rel, err := filepath.Rel(absRepo, absFile)
	if err != nil {
		return fmt.Errorf("checking file location: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("file %s is outside repository %s", filePath, repoDir)
	}
	return nil
}