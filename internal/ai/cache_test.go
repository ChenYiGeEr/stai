package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", repo, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// git rev-parse --git-dir returns relative path when inside repo
	got, err = ReviewCachePath(repo)
	if err != nil {
		t.Fatalf("ReviewCachePath(repo) error: %v", err)
	}
	// Accept relative .git path (git rev-parse --git-dir returns relative when inside repo)
	if got != ".git/stai-review-cache.jsonl" &&
		got != filepath.Join(repo, ".git", "stai-review-cache.jsonl") {
		t.Errorf("ReviewCachePath(repo) = %q, want relative or absolute .git path", got)
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
	// Create a bare repo for the main gitdir
	mainRepo := filepath.Join(dir, "main.git")
	if _, err := exec.Command("git", "init", "--bare", mainRepo).Output(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	// Add worktree (git uses the base name of the path as the worktree name)
	if _, err := exec.Command("git", "-C", mainRepo, "worktree", "add", worktree).Output(); err != nil {
		t.Fatalf("git worktree add: %v", err)
	}
	// The actual gitdir will be main.git/worktrees/worktree (base name of worktree path)
	actualGitdir := filepath.Join(dir, "main.git", "worktrees", "worktree")
	want := filepath.Join(actualGitdir, "stai-review-cache.jsonl")
	got, err = ReviewCachePath(worktree)
	if err != nil {
		t.Fatalf("ReviewCachePath(worktree) error: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var) before comparing
	gotResolved, _ := filepath.EvalSymlinks(got)
	wantResolved, _ := filepath.EvalSymlinks(want)
	if gotResolved != wantResolved {
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
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	path := filepath.Join(dir, "cache.jsonl")

	findings := []Finding{
		{Severity: "high", Location: "a.go:1", Message: "bug"},
	}
	key := "key1"
	entries := []reviewCacheEntry{{Key: key, Findings: findings, Timestamp: time.Now()}}
	if err := SaveReviewCache(path, entries, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadReviewCache(path, dir)
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
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
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
	if err := SaveReviewCache(path, entries, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadReviewCache(path, dir)
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
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	path := filepath.Join(dir, "missing", "cache.jsonl")
	// dir exists but missing/ does not; os.CreateTemp + MkdirAll should create it.
	if err := SaveReviewCache(path, []reviewCacheEntry{{Key: "k", Timestamp: time.Now()}}, dir); err != nil {
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
	// Initialize a real git repo
	if _, err := exec.Command("git", "-C", dir, "init").Output(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	path := filepath.Join(dir, "cache.jsonl")

	// Create valid entries using the API (they will have MAC)
	good1 := reviewCacheEntry{Key: "good1", Timestamp: time.Now(), Findings: []Finding{{Message: "first"}}}
	good2 := reviewCacheEntry{Key: "good2", Timestamp: time.Now(), Findings: []Finding{{Message: "second"}}}
	if err := SaveReviewCache(path, []reviewCacheEntry{good1, good2}, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Manually append a huge line to the file
	huge := strings.Repeat("x", reviewCacheMaxSize+1024)
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

	entries, err := LoadReviewCache(path, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Key != "good1" && e.Key != "good2" {
			t.Errorf("unexpected entry %q", e.Key)
		}
	}
}
