//go:build !darwin

package main

import (
	"context"
	"errors"

	"stai/internal/i18n"
)

// openMergetoolTerminal refuses to open a terminal outside macOS.
// The 2026-11-12 slice only requires Terminal.app. Nothing is written.
func openMergetoolTerminal(_ context.Context, _, _, _, _ string) (int, error) {
	return 1, errors.New(i18n.T("mergetool_terminal_unavailable"))
}
