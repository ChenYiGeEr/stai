//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// openMergetoolTerminal asks Terminal.app to run the same mergetool command
// and waits for that session's exit code. It does not write $MERGED.
func openMergetoolTerminal(ctx context.Context, base, local, remote, merged string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, err := os.MkdirTemp("", "stai-mergetool-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)
	status := filepath.Join(dir, "status")
	command := mergetoolTerminalCommand(exe, base, local, remote, merged, status)
	apple := "tell application \"Terminal\"\nactivate\ndo script " + osaString(command) + "\nend tell\n"
	cmd := exec.CommandContext(ctx, "osascript", "-e", apple)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return 1, nil
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return 1, fmt.Errorf("%s", msg)
	}
	return waitMergetoolStatus(ctx, status), nil
}
