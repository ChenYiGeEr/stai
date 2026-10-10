// Package color provides minimal ANSI styling helpers for terminal output.
// It automatically disables colors when the NO_COLOR environment variable is
// set, so CI and accessibility users get plain text.
package color

import "os"

var disabled = os.Getenv("NO_COLOR") != ""

func wrap(code, s string) string {
	if disabled {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// Enabled reports whether colors are active.
func Enabled() bool { return !disabled }

// Bold returns s in bold.
func Bold(s string) string { return wrap("1", s) }

// Cyan returns s in cyan.
func Cyan(s string) string { return wrap("36", s) }

// Green returns s in green.
func Green(s string) string { return wrap("32", s) }

// Yellow returns s in yellow.
func Yellow(s string) string { return wrap("33", s) }

// Red returns s in red.
func Red(s string) string { return wrap("31", s) }

// Gray returns s in bright black (gray).
func Gray(s string) string { return wrap("90", s) }
