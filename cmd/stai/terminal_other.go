//go:build !darwin && !linux && !windows

package main

import "fmt"

// isTerminal reports false on unsupported platforms.
func isTerminal(fd uintptr) bool { return false }

// disableEcho returns an error so readPassword falls back to a visible prompt.
func disableEcho() error { return fmt.Errorf("hidden input not supported on this platform") }

// enableEcho is a no-op on unsupported platforms.
func enableEcho() error { return nil }
