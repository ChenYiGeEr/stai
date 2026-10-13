package ai

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"stai/internal/git"
)

const (
	sessionMaxSize = 10 * 1024 * 1024   // 10 MB, same cap as the review cache
	sessionMaxAge  = 7 * 24 * time.Hour // same TTL as the review cache
	sessionFile    = "stai-review-sessions.jsonl"
)

// reviewSession records the progress of one review run: which groups
// completed and what they found. It lets an interrupted review resume
// instead of re-running every group.
type reviewSession struct {
	ID              string    `json:"id"` // the review cache key for this run
	Timestamp       time.Time `json:"ts"`
	CompletedGroups []int     `json:"completed_groups"` // 1-based group numbers
	TotalGroups     int       `json:"total_groups"`
	Findings        []Finding `json:"findings"`
	Error           string    `json:"error,omitempty"` // the error that stopped the run, if any
	MAC             string    `json:"mac,omitempty"`   // HMAC-SHA256 of session data
}

// ReviewSessionPath returns the per-repo session file path, or "" when
// repoRoot is empty. It handles git worktrees by using `git rev-parse --git-dir`
// to avoid symlink attacks.
func ReviewSessionPath(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	gitDir, err := git.GitDir(repoRoot)
	if err != nil {
		return "", fmt.Errorf("locating .git: %w", err)
	}
	return filepath.Join(gitDir, sessionFile), nil
}

// LoadReviewSessions reads session entries from path. Malformed lines are
// skipped; lines longer than sessionMaxSize are consumed but skipped, so a
// corrupt file cannot force an unbounded allocation.
// Entries with invalid or missing MAC are rejected (treated as corrupted).
func LoadReviewSessions(path string, repoRoot string) ([]reviewSession, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []reviewSession
	rd := bufio.NewReader(f)
	for {
		line, truncated, err := readBoundedLine(rd, sessionMaxSize)
		if len(line) > 0 && !truncated {
			line = bytes.TrimRight(line, "\r\n")
			var e reviewSession
			if json.Unmarshal(line, &e) == nil && e.ID != "" {
				if verifySessionMAC(repoRoot, &e) {
					entries = append(entries, e)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	return entries, nil
}

// SaveReviewSessions writes session entries atomically, creating parent dirs.
// Each entry is signed with HMAC using the per-repo secret.
func SaveReviewSessions(path string, entries []reviewSession, repoRoot string) error {
	if path == "" {
		return nil
	}
	// Clone entries to avoid modifying caller's slice
	clone := append([]reviewSession(nil), entries...)
	secret, err := repoSecret(repoRoot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Add MAC to each entry
	for i := range clone {
		clone[i].MAC = computeSessionMAC(secret, &clone[i])
	}

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	bw := bufio.NewWriter(f)
	for _, e := range clone {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := bw.Write(b); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// computeSessionMAC computes HMAC-SHA256 of the session entry data.
// Uses JSON serialization to avoid delimiter collision issues.
func computeSessionMAC(secret []byte, s *reviewSession) string {
	// Create a struct without MAC for deterministic serialization
	data := struct {
		ID              string    `json:"id"`
		Timestamp       time.Time `json:"ts"`
		CompletedGroups []int     `json:"completed_groups"`
		TotalGroups     int       `json:"total_groups"`
		Findings        []Finding `json:"findings"`
		Error           string    `json:"error,omitempty"`
	}{
		ID:              s.ID,
		Timestamp:       s.Timestamp,
		CompletedGroups: s.CompletedGroups,
		TotalGroups:     s.TotalGroups,
		Findings:        s.Findings,
		Error:           s.Error,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// verifySessionMAC verifies the HMAC of a session entry.
func verifySessionMAC(repoRoot string, s *reviewSession) bool {
	if s.MAC == "" {
		return false // entries without MAC are rejected
	}
	secret, err := repoSecret(repoRoot)
	if err != nil {
		return false
	}
	expected := computeSessionMAC(secret, s)
	return hmac.Equal([]byte(expected), []byte(s.MAC))
}

// CompactReviewSessions removes expired entries and deduplicates by ID
// (keeping the newest), trimming the file under the size cap.
func CompactReviewSessions(entries []reviewSession, repoRoot string) []reviewSession {
	secret, err := repoSecret(repoRoot)
	if err != nil {
		return entries
	}
	cutoff := time.Now().Add(-sessionMaxAge)
	latest := make(map[string]reviewSession)
	for _, e := range entries {
		if e.Timestamp.Before(cutoff) || e.Timestamp.IsZero() {
			continue
		}
		if existing, ok := latest[e.ID]; !ok || e.Timestamp.After(existing.Timestamp) {
			latest[e.ID] = e
		}
	}
	kept := make([]reviewSession, 0, len(latest))
	for _, e := range latest {
		kept = append(kept, e)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Timestamp.After(kept[j].Timestamp) })

	var size int64
	for i, e := range kept {
		b, err := json.Marshal(e)
		if err != nil {
			return entries
		}
		size += int64(len(b)) + 1
		if size > sessionMaxSize {
			kept = kept[:i]
			break
		}
	}
	// Add MAC to kept entries
	for i := range kept {
		kept[i].MAC = computeSessionMAC(secret, &kept[i])
	}
	return kept
}

// UpsertReviewSession inserts or replaces the entry with the same ID and
// persists the compacted list. It rejects entries that exceed the per-line
// size cap so callers know the session cannot be recovered, instead of
// writing a line that LoadReviewSessions would silently skip.
func UpsertReviewSession(path string, entry reviewSession, repoRoot string) error {
	secret, err := repoSecret(repoRoot)
	if err != nil {
		return err
	}
	// Add MAC to entry first, then check size
	entry.MAC = computeSessionMAC(secret, &entry)
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(b) > sessionMaxSize {
		return fmt.Errorf("review session entry too large (%d bytes, limit %d)", len(b), sessionMaxSize)
	}
	entries, err := LoadReviewSessions(path, repoRoot)
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == entry.ID {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return SaveReviewSessions(path, CompactReviewSessions(entries, repoRoot), repoRoot)
}

// DeleteReviewSession removes the entry with the given ID.
func DeleteReviewSession(path, id string, repoRoot string) error {
	entries, err := LoadReviewSessions(path, repoRoot)
	if err != nil || len(entries) == 0 {
		return err
	}
	kept := entries[:0]
	removed := false
	for _, e := range entries {
		if e.ID == id {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return nil
	}
	return SaveReviewSessions(path, kept, repoRoot)
}

// FindReviewSession returns the session whose ID starts with shortID.
// It fails when no entry matches or the prefix is ambiguous.
func FindReviewSession(path, shortID string, repoRoot string) (*reviewSession, error) {
	entries, err := LoadReviewSessions(path, repoRoot)
	if err != nil {
		return nil, err
	}
	var found *reviewSession
	for i := range entries {
		if len(entries[i].ID) >= len(shortID) && entries[i].ID[:len(shortID)] == shortID {
			if found != nil {
				return nil, fmt.Errorf("session id %q is ambiguous; use more characters", shortID)
			}
			found = &entries[i]
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no session matches id %q (sessions expire after 7 days)", shortID)
	}
	return found, nil
}

// ShortSessionID returns the user-facing 8-character prefix of a session id.
func ShortSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
