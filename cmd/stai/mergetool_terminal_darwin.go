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

// openMergetoolTerminal opens Terminal.app on a .command file and waits for
// that session's exit code. It does not write $MERGED and does not send
// Apple events to Terminal.
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
	// .command is the extension Terminal.app executes when opened as a document.
	scriptPath := filepath.Join(dir, "stai-mergetool.command")
	body := mergetoolCommandScript(exe, base, local, remote, merged, status)
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		return 1, err
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		return 1, err
	}
	argv := terminalOpenArgv(scriptPath)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
