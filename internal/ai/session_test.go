package ai

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpsertReviewSessionRejectsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.jsonl"

	entry := reviewSession{
		ID:              "big",
		Timestamp:       time.Now(),
		CompletedGroups: []int{1},
		TotalGroups:     1,
		// A giant message makes the JSON line exceed sessionMaxSize.
		Findings: []Finding{{
			Severity: "low",
			Location: "a.go:1",
			Message:  strings.Repeat("x", sessionMaxSize+1024),
		}},
	}
	if err := UpsertReviewSession(path, entry); err == nil {
		t.Fatalf("expected oversized session to be rejected")
	}
}

func TestLoadReviewSessionsSkipsOverlongLine(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.jsonl"

	good := reviewSession{ID: "good", Timestamp: time.Now()}
	goodBytes, err := json.Marshal(good)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	huge := strings.Repeat("x", sessionMaxSize+1024)

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write(goodBytes); err != nil {
		t.Fatalf("write good: %v", err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if _, err := f.WriteString(huge); err != nil {
		t.Fatalf("write huge: %v", err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if _, err := f.Write(goodBytes); err != nil {
		t.Fatalf("write good: %v", err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatalf("write newline: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := LoadReviewSessions(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}
