package color

import (
	"os"
	"strings"
	"testing"
)

func TestColorsAddEscapeCodes(t *testing.T) {
	old := disabled
	t.Cleanup(func() { disabled = old })
	disabled = false
	s := Cyan("hello")
	if !strings.Contains(s, "\033[") {
		t.Errorf("expected ANSI escape in %q", s)
	}
}

func TestNoColorDisablesEscapes(t *testing.T) {
	old := disabled
	t.Cleanup(func() { disabled = old })
	t.Setenv("NO_COLOR", "1")
	disabled = os.Getenv("NO_COLOR") != ""
	s := Cyan("hello")
	if strings.Contains(s, "\033[") {
		t.Errorf("expected no ANSI escape with NO_COLOR, got %q", s)
	}
}
