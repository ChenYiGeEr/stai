package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewCachePath(t *testing.T) {
	got, err := ReviewCachePath("")
	if err != nil {
		t.Fatalf("ReviewCachePath(\"\") error: %v", err)
	}
	if got != "" {
		t.Errorf("ReviewCachePath(\"\") = %q, want empty", got)
	}

	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	want := filepath.Join(repo, ".git", "stai-review-cache.jsonl")
	got, err = ReviewCachePath(repo)
	if err != nil {
		t.Fatalf("ReviewCachePath(repo) error: %v", err)
	}
	if got != want {
		t.Errorf("ReviewCachePath(repo) = %q, want %q", got, want)
	}

	// Worktree: .git is a file pointing to gitdir.
	worktree := filepath.Join(dir, "worktree")
	gitdir := filepath.Join(dir, "main.git", "worktrees", "worktree")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("mkdir gitdir: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}
	want = filepath.Join(gitdir, "stai-review-cache.jsonl")
	got, err = ReviewCachePath(worktree)
	if err != nil {
		t.Fatalf("ReviewCachePath(worktree) error: %v", err)
	}
	if got != want {
		t.Errorf("ReviewCachePath(worktree) = %q, want %q", got, want)
	}
}

func TestMakeReviewCacheKeyIsStable(t *testing.T) {
	rules := []string{"rule1", "rule2"}
	k1 := MakeReviewCacheKey([]byte("diff"), "model", "http://x", 0.5, rules, true, false, "zh-CN", true, true, 50, 0, false, nil, false)
	k2 := MakeReviewCacheKey([]byte("diff"), "model", "http://x", 0.5, rules, true, false, "zh-CN", true, true, 50, 0, false, nil, false)
	if k1 != k2 {
		t.Errorf("cache key not stable: %q vs %q", k1, k2)
	}
	k3 := MakeReviewCacheKey([]byte("diff"), "model", "http://x", 0.5, rules, true, false, "zh-CN", true, false, 50, 0, false, nil, false)
	if k1 == k3 {
		t.Errorf("seed flag change should alter key")
	}
	k4 := MakeReviewCacheKey([]byte("diff"), "model", "http://x", 0.5, rules, true, false, "zh-CN", true, true, 100, 0, false, nil, false)
	if k1 == k4 {
		t.Errorf("group max lines change should alter key")
	}
}

func TestReviewCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	findings := []Finding{
		{Severity: "high", Location: "a.go:1", Message: "bug"},
	}
	key := "key1"
	entries := []reviewCacheEntry{{Key: key, Findings: findings, Timestamp: time.Now()}}
	if err := SaveReviewCache(path, entries); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadReviewCache(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].Findings) != 1 {
		t.Fatalf("unexpected entries: %+v", loaded)
	}
	got := FindCachedReview(loaded, key)
	if len(got) != 1 || got[0].Message != "bug" {
		t.Errorf("FindCachedReview = %+v", got)
	}
}

func TestCompactReviewCacheDeduplicates(t *testing.T) {
	key := "same-key"
	now := time.Now()
	// Both duplicates are inside the retention window, so this test really
	// exercises the dedup logic, not the TTL eviction.
	entries := []reviewCacheEntry{
		{Key: key, Findings: []Finding{{Message: "older"}}, Timestamp: now.Add(-time.Hour)},
		{Key: key, Findings: []Finding{{Message: "newest"}}, Timestamp: now},
		{Key: "fresh", Findings: []Finding{{Message: "ok"}}, Timestamp: now},
	}
	out := CompactReviewCache(entries)
	count := 0
	for _, e := range out {
		if e.Key != key {
			continue
		}
		count++
		if e.Findings[0].Message != "newest" {
			t.Errorf("did not keep newest duplicate")
		}
	}
	if count != 1 {
		t.Fatalf("same-key appears %d times, want exactly 1", count)
	}
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}
}

func TestCompactReviewCacheEvictsExpired(t *testing.T) {
	now := time.Now()
	entries := []reviewCacheEntry{
		{Key: "expired", Findings: []Finding{{Message: "old"}}, Timestamp: now.Add(-30 * 24 * time.Hour)},
		{Key: "fresh", Findings: []Finding{{Message: "ok"}}, Timestamp: now},
	}
	out := CompactReviewCache(entries)
	if len(out) != 1 || out[0].Key != "fresh" {
		t.Fatalf("expired entry not evicted: %+v", out)
	}
}

func TestReviewCacheSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	// Write several large entries whose combined size exceeds the cache limit.
	bigMessage := strings.Repeat("x", 1024*1024) // 1 MiB
	now := time.Now()
	entries := make([]reviewCacheEntry, 12)
	for i := range entries {
		entries[i] = reviewCacheEntry{
			Key:       fmt.Sprintf("big-%d", i),
			Findings:  []Finding{{Message: bigMessage}},
			Timestamp: now.Add(-time.Duration(i) * time.Second),
		}
	}
	if err := SaveReviewCache(path, entries); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadReviewCache(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	trimmed := CompactReviewCache(loaded)
	var total int64
	for _, e := range trimmed {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		total += int64(len(b)) + 1
	}
	if total > reviewCacheMaxSize {
		t.Errorf("expected trimmed cache under %d bytes, got %d entries totaling %d", reviewCacheMaxSize, len(trimmed), total)
	}
}

func TestReviewCacheCreatesMissingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "cache.jsonl")
	// dir exists but missing/ does not; os.CreateTemp + MkdirAll should create it.
	if err := SaveReviewCache(path, []reviewCacheEntry{{Key: "k", Timestamp: time.Now()}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cache file not created: %v", err)
	}
}

func TestMakeReviewCacheKeyNoSeparatorCollision(t *testing.T) {
	// With raw "|" concatenation these two inputs produced the same key;
	// JSON serialization must keep them distinct.
	k1 := MakeReviewCacheKey([]byte("d"), "m", "http://x", 0, []string{"x|y"}, true, false, "en", true, true, 10, 0, false, nil, false)
	k2 := MakeReviewCacheKey([]byte("d"), "m", "http://x", 0, []string{"x", "y"}, true, false, "en", true, true, 10, 0, false, nil, false)
	if k1 == k2 {
		t.Fatalf("keys collided for rules [x|y] vs [x y]")
	}
}

func TestMakeReviewCacheKeyIncludesMaxCharsAndValidateLocations(t *testing.T) {
	base := MakeReviewCacheKey([]byte("d"), "m", "http://x", 0, nil, false, false, "en", true, true, 10, 0, false, nil, false)
	withMaxChars := MakeReviewCacheKey([]byte("d"), "m", "http://x", 0, nil, false, false, "en", true, true, 10, 100, false, nil, false)
	withValidate := MakeReviewCacheKey([]byte("d"), "m", "http://x", 0, nil, false, false, "en", true, true, 10, 0, true, nil, false)
	if base == withMaxChars {
		t.Errorf("max_chars change should alter cache key")
	}
	if base == withValidate {
		t.Errorf("validate_locations change should alter cache key")
	}
}

func TestReviewCacheSkipsOverlongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	good := reviewCacheEntry{Key: "good", Timestamp: time.Now()}
	goodBytes, err := json.Marshal(good)
	if err != nil {
		t.Fatalf("marshal good: %v", err)
	}
	// A line far larger than reviewCacheMaxSize would OOM if ReadBytes'd.
	huge := strings.Repeat("x", reviewCacheMaxSize+1024)

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

	entries, err := LoadReviewCache(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Key != "good" {
			t.Errorf("unexpected entry %q", e.Key)
		}
	}
}
