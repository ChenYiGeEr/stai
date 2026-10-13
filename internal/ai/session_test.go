package ai

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestUpsertReviewSessionRejectsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
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
	if err := UpsertReviewSession(path, entry, dir); err == nil {
		t.Fatalf("expected oversized session to be rejected")
	}
}

func TestLoadReviewSessionsSkipsOverlongLine(t *testing.T) {
	dir := t.TempDir()
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	path := dir + "/sessions.jsonl"

	// Create valid entries using the API (they will have MAC)
	good1 := reviewSession{ID: "good1", Timestamp: time.Now(), CompletedGroups: []int{1}, TotalGroups: 1}
	good2 := reviewSession{ID: "good2", Timestamp: time.Now(), CompletedGroups: []int{1}, TotalGroups: 1}
	if err := UpsertReviewSession(path, good1, dir); err != nil {
		t.Fatalf("upsert good1: %v", err)
	}
	if err := UpsertReviewSession(path, good2, dir); err != nil {
		t.Fatalf("upsert good2: %v", err)
	}

	// Manually append a huge line to the file
	huge := strings.Repeat("x", sessionMaxSize+1024)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(huge + "\n"); err != nil {
		t.Fatalf("write huge: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := LoadReviewSessions(path, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.ID != "good1" && e.ID != "good2" {
			t.Errorf("unexpected entry %q", e.ID)
		}
	}
}
