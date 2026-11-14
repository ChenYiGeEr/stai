package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// launchMergetoolTerminal opens the interactive session when stdin is not a
// TTY. Tests replace it. The default opens Terminal.app on macOS and refuses
// everywhere else.
var launchMergetoolTerminal = openMergetoolTerminal

// stdinIsTerminal reports whether r is an os.File attached to a terminal.
// Any other reader, including a pipe used by tests, is not a terminal.
func stdinIsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(f.Fd())
}

// mergetoolTerminalCommand is the shell command Terminal.app runs. It sets
// STAI_MERGETOOL_TERMINAL so the child does not open another terminal, then
// records the child's exit code for the parent to relay.
func mergetoolTerminalCommand(exe, base, local, remote, merged, status string) string {
	return strings.Join([]string{
		"STAI_MERGETOOL_TERMINAL=1",
		shellQuote(exe),
		"mergetool",
		"--base", shellQuote(base),
		"--local", shellQuote(local),
		"--remote", shellQuote(remote),
		"--merged", shellQuote(merged),
		";",
		"code=$?",
		";",
		"echo \"$code\" > " + shellQuote(status),
		";",
		"exit \"$code\"",
	}, " ")
}

// mergetoolCommandScript is a Terminal .command file. Terminal.app executes
// it as a document opened via Launch Services, so the window stays up for
// the whole interactive session.
func mergetoolCommandScript(exe, base, local, remote, merged, status string) string {
	return "#!/bin/sh\n" + mergetoolTerminalCommand(exe, base, local, remote, merged, status) + "\n"
}

// terminalOpenArgv launches Terminal.app on a .command file. This is
// /usr/bin/open, not an Apple event such as `tell application "Terminal"`.
// SourceTree-started stai is a separate process; Apple events to Terminal
// are refused silently (osascript -1743) when that process has no Automation
// grant, and the window flashes closed. Opening a document does not ask
// Terminal to be controlled.
func terminalOpenArgv(scriptPath string) []string {
	return []string{"/usr/bin/open", "-a", "Terminal", scriptPath}
}

func readMergetoolStatus(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return n, true
}

func waitMergetoolStatus(ctx context.Context, path string) int {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if code, ok := readMergetoolStatus(path); ok {
			return code
		}
		select {
		case <-ctx.Done():
			if code, ok := readMergetoolStatus(path); ok {
				return code
			}
			return 1
		case <-ticker.C:
		}
	}
}
