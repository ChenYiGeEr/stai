package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"stai/internal/i18n"
)

// sourceTreeProcessNames are the only names install and doctor treat as a
// running SourceTree app. The match is case-sensitive and exact (pgrep -x):
// no window title, plist lock, bundle path, case folding, or substring.
//
// Sourcetree is the process name verified on SourceTree 4.2.20.
// SourceTree is kept as an exact-name compatibility match. Keeping it does
// not claim that any current version was verified to still use that spelling.
var sourceTreeProcessNames = []string{"Sourcetree", "SourceTree"}

// pgrepExact reports whether a process with this exact name is running.
// pgrep -x exits 1 when nothing matches.
var pgrepExact = func(name string) (bool, error) {
	cmd := exec.Command("pgrep", "-x", name)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// sourceTreeInstallInteractive reports whether install should print the
// running-app warning as a prominent banner. Tests replace it.
var sourceTreeInstallInteractiveFn = func() bool {
	return isTerminal(os.Stdin.Fd())
}

func sourceTreeProcessesRunning() ([]string, error) {
	var hit []string
	for _, name := range sourceTreeProcessNames {
		ok, err := pgrepExact(name)
		if err != nil {
			return nil, err
		}
		if ok {
			hit = append(hit, name)
		}
	}
	return hit, nil
}

func sourceTreeRunningWarning() string {
	return i18n.T("sourcetree_running_warn")
}

// formatSourceTreeRunningWarning is the install warning. Interactive runs
// get a banner on stderr so it is not buried in the log; other runs still
// print the same sentence on stderr.
func formatSourceTreeRunningWarning(interactive bool) string {
	msg := sourceTreeRunningWarning()
	if !interactive {
		return "stai: " + msg + "\n"
	}
	bar := strings.Repeat("!", 60)
	return "\n" + bar + "\n" +
		"stai: " + i18n.T("sourcetree_running_label") + "\n" +
		msg + "\n" +
		bar + "\n\n"
}

// warnIfSourceTreeRunning prints the running-app warning when Sourcetree or
// SourceTree is running. It never changes the exit code. A check failure is
// logged and does not block install or doctor.
func warnIfSourceTreeRunning(interactive bool) {
	running, err := sourceTreeProcessesRunning()
	if err != nil {
		logf("sourcetree process check: %v", err)
		return
	}
	if len(running) == 0 {
		return
	}
	fmt.Fprint(os.Stderr, formatSourceTreeRunningWarning(interactive))
}

// warnIfSourceTreeRunningDoctor prints the same sentence in the doctor report.
func warnIfSourceTreeRunningDoctor() {
	running, err := sourceTreeProcessesRunning()
	if err != nil {
		logf("sourcetree process check: %v", err)
		return
	}
	if len(running) == 0 {
		return
	}
	warn(sourceTreeRunningWarning())
}
