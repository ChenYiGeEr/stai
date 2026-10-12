package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"stai/internal/config"
)

// logPath is the diagnostic log file: the default until the config is
// loaded, then [log] path from the config.
var logPath = config.Default().Log.Path

// logf appends a timestamped line to the log file. SourceTree discards
// custom-action output, so this is the only record of why a run failed.
// Logging is best-effort and never affects the exit path.
func logf(format string, args ...any) {
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	// Escape newlines to prevent log injection/falsification
	msg = strings.ReplaceAll(msg, "\n", "\\n")
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}