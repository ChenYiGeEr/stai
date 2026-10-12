package ai

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stai/internal/config"
)

const (
	reviewCacheMaxSize = 10 * 1024 * 1024 // 10 MB
	reviewCacheMaxAge  = 7 * 24 * time.Hour
	reviewCacheFile    = "stai-review-cache.jsonl"
)

// reviewCacheEntry is one cached review result.
type reviewCacheEntry struct {
	Key       string    `json:"key"`
	Findings  []Finding `json:"findings"`
	Timestamp time.Time `json:"ts"`
}

// ReviewCachePath returns the per-repo cache path, or "" when repoRoot is empty.
// It handles git worktrees where .git is a file pointing to the real gitdir.
func ReviewCachePath(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	gitPath := filepath.Join(repoRoot, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("locating .git: %w", err)
	}
	if fi.IsDir() {
		return filepath.Join(gitPath, reviewCacheFile), nil
	}

	// .git is a file in a worktree; parse the gitdir line.
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("reading .git file: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gitdir:") {
			gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
			if gitdir == "" {
				return "", fmt.Errorf("empty gitdir in .git file")
			}
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(repoRoot, gitdir)
			}
			return filepath.Join(gitdir, reviewCacheFile), nil
		}
	}
	return "", fmt.Errorf("gitdir not found in .git file")
}

// MakeReviewCacheKey builds a deterministic cache key from all inputs that
// affect the review output. It must stay stable across stai versions for the
// same effective review request. The inputs are JSON-serialized (no raw
// separator concatenation), so field values containing "|" or other
// separators cannot collide with a different input.
func MakeReviewCacheKey(diff []byte, model, baseURL string, temperature float64, rules []string, suggestFixes, strict bool, lang string, deterministic, seed bool, groupMaxLines, maxChars int, validateLocations bool, ruleSets []config.ReviewRule, bundle bool) string {
	input := struct {
		DiffSHA256        string              `json:"diff_sha256"`
		Model             string              `json:"model"`
		BaseURL           string              `json:"base_url"`
		Temperature       float64             `json:"temperature"`
		Rules             []string            `json:"rules"`
		SuggestFixes      bool                `json:"suggest_fixes"`
		Strict            bool                `json:"strict"`
		Lang              string              `json:"lang"`
		Deterministic     bool                `json:"deterministic"`
		Seed              bool                `json:"seed"`
		GroupMaxLines     int                 `json:"group_max_lines"`
		MaxChars          int                 `json:"max_chars"`
		ValidateLocations bool                `json:"validate_locations"`
		RuleSets          []config.ReviewRule `json:"rule_sets"`
		Bundle            bool                `json:"bundle"`
	}{
		DiffSHA256:        fmt.Sprintf("%x", sha256.Sum256(diff)),
		Model:             model,
		BaseURL:           baseURL,
		Temperature:       temperature,
		Rules:             rules,
		SuggestFixes:      suggestFixes,
		Strict:            strict,
		Lang:              lang,
		Deterministic:     deterministic,
		Seed:              seed,
		GroupMaxLines:     groupMaxLines,
		MaxChars:          maxChars,
		ValidateLocations: validateLocations,
		RuleSets:          ruleSets,
		Bundle:            bundle,
	}
	b, err := json.Marshal(input)
	if err != nil {
		// json.Marshal cannot fail on this shape; fall back to printing the
		// struct so a key is still produced rather than panicking.
		b = []byte(fmt.Sprintf("%+v", input))
	}
	h := sha256.New()
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// readBoundedLine reads one \n-terminated line from r without ever holding
// more than max bytes of it in memory: longer lines are consumed (streamed
// past) but only their first max bytes are returned, with truncated=true.
// Callers skip truncated lines — they cannot parse as JSON anyway — so a
// corrupt or malicious file cannot force an unbounded allocation.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, truncated bool, err error) {
	var buf []byte
	for {
		chunk, rerr := r.ReadSlice('\n')
		if len(buf) < max {
			take := len(chunk)
			if len(buf)+take > max {
				take = max - len(buf)
				truncated = true
			}
			buf = append(buf, chunk[:take]...)
		} else if len(chunk) > 0 {
			truncated = true
		}
		if rerr == bufio.ErrBufferFull {
			continue // line longer than the read buffer; keep consuming it
		}
		if rerr != nil {
			if rerr == io.EOF && len(buf) > 0 {
				return buf, truncated, nil
			}
			return buf, truncated, rerr
		}
		return buf, truncated, nil
	}
}

// LoadReviewCache reads cached entries from path. Malformed lines are skipped.
func LoadReviewCache(path string) ([]reviewCacheEntry, error) {
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

	var entries []reviewCacheEntry
	rd := bufio.NewReader(f)
	for {
		line, truncated, err := readBoundedLine(rd, reviewCacheMaxSize)
		if len(line) > 0 && !truncated {
			line = bytes.TrimRight(line, "\r\n")
			var e reviewCacheEntry
			if json.Unmarshal(line, &e) == nil && e.Key != "" {
				entries = append(entries, e)
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

// SaveReviewCache writes entries to path atomically, creating parent dirs.
func SaveReviewCache(path string, entries []reviewCacheEntry) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	bw := bufio.NewWriter(f)
	for _, e := range entries {
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

// CompactReviewCache removes expired entries, deduplicates by key (keeping the
// newest), and trims the cache so it stays under the size limit.
func CompactReviewCache(entries []reviewCacheEntry) []reviewCacheEntry {
	cutoff := time.Now().Add(-reviewCacheMaxAge)

	// Deduplicate by key, keep the newest timestamp.
	latest := make(map[string]reviewCacheEntry)
	for _, e := range entries {
		if e.Timestamp.Before(cutoff) || e.Timestamp.IsZero() {
			continue
		}
		if existing, ok := latest[e.Key]; !ok || e.Timestamp.After(existing.Timestamp) {
			latest[e.Key] = e
		}
	}

	kept := make([]reviewCacheEntry, 0, len(latest))
	for _, e := range latest {
		kept = append(kept, e)
	}

	// Newest first so trimming keeps the most recently used entries.
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].Timestamp.After(kept[j].Timestamp)
	})

	var size int64
	n := 0
	for _, e := range kept {
		b, err := json.Marshal(e)
		if err != nil {
			continue // drop unserializable entry; keep the cleanup work done so far
		}
		if size+int64(len(b))+1 > reviewCacheMaxSize {
			break
		}
		size += int64(len(b)) + 1
		kept[n] = e
		n++
	}
	return kept[:n]
}

// FindCachedReview returns a matching cached review, or nil if none.
// The returned slice is a copy; callers may modify it without affecting cache.
func FindCachedReview(entries []reviewCacheEntry, key string) []Finding {
	for _, e := range entries {
		if e.Key == key {
			return append([]Finding(nil), e.Findings...)
		}
	}
	return nil
}
