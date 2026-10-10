//go:build windows

package main

import "fmt"

// isTerminal always reports false on Windows: stai is designed for macOS and
// its SourceTree integration, so the interactive wizard is not supported here.
func isTerminal(fd uintptr) bool { return false }

// disableEcho returns an error so readPassword falls back to a visible prompt.
func disableEcho() error { return fmt.Errorf("hidden input not supported on Windows") }

// enableEcho is a no-op on Windows.
func enableEcho() error { return nil }
