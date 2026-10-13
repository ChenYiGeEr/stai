package ai

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"stai/internal/config"
	"stai/internal/git"
)

const (
	reviewCacheMaxSize = 10 * 1024 * 1024 // 10 MB
	reviewCacheMaxAge  = 7 * 24 * time.Hour
	reviewCacheFile    = "stai-review-cache.jsonl"
	reviewSecretFile   = "stai-review-secret.jsonl" // stores per-repo HMAC secret keys
)

// reviewCacheEntry is one cached review result.
type reviewCacheEntry struct {
	Key       string    `json:"key"`
	Findings  []Finding `json:"findings"`
	Timestamp time.Time `json:"ts"`
	MAC       string    `json:"mac,omitempty"` // HMAC-SHA256 of key+findings+timestamp
	KeyID     string    `json:"key_id,omitempty"` // identifies which secret key was used
}

// ReviewCachePath returns the per-repo cache path, or "" when repoRoot is empty.
// It handles git worktrees by using `git rev-parse --git-dir` to avoid symlink attacks.
func ReviewCachePath(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	gitDir, err := git.GitDir(repoRoot)
	if err != nil {
		return "", fmt.Errorf("locating .git: %w", err)
	}
	return filepath.Join(gitDir, reviewCacheFile), nil
}

// secretKeyPath returns the path to the secret key file for the repo.
func secretKeyPath(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", nil
	}
	gitDir, err := git.GitDir(repoRoot)
	if err != nil {
		return "", fmt.Errorf("locating .git: %w", err)
	}
	return filepath.Join(gitDir, reviewSecretFile), nil
}

// secretKeyEntry represents a single secret key entry.
type secretKeyEntry struct {
	KeyID string `json:"key_id"`
	Key   []byte `json:"key"`
}

// loadOrCreateSecret loads the current secret key for the repo, or creates a new one.
func loadOrCreateSecret(repoRoot string) (keyID string, key []byte, err error) {
	path, err := secretKeyPath(repoRoot)
	if err != nil {
		return "", nil, err
	}
	if path == "" {
		return "", nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Create new secret key
			return createSecretKey(path)
		}
		return "", nil, err
	}
	defer f.Close()

	var entries []secretKeyEntry
	rd := bufio.NewReader(f)
	for {
		line, truncated, err := readBoundedLine(rd, reviewCacheMaxSize)
		if len(line) > 0 && !truncated {
			line = bytes.TrimRight(line, "\r\n")
			var e secretKeyEntry
			if json.Unmarshal(line, &e) == nil && e.KeyID != "" {
				entries = append(entries, e)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", nil, err
		}
	}

	// Use the latest key (last entry)
	if len(entries) > 0 {
		latest := entries[len(entries)-1]
		return latest.KeyID, latest.Key, nil
	}

	// No valid keys found, create new one
	return createSecretKey(path)
}

// createSecretKey generates a new random secret key and saves it.
func createSecretKey(path string) (string, []byte, error) {
	keyID := make([]byte, 16)
	if _, err := rand.Read(keyID); err != nil {
		return "", nil, err
	}
	keyIDStr := fmt.Sprintf("%x", keyID)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", nil, err
	}

	entry := secretKeyEntry{KeyID: keyIDStr, Key: key}
	b, err := json.Marshal(entry)
	if err != nil {
		return "", nil, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}

	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", nil, err
	}
	tmp := f.Name()
	bw := bufio.NewWriter(f)
	if _, err := bw.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", nil, err
	}
	if err := bw.WriteByte('\n'); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", nil, err
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", nil, err
	}
	return keyIDStr, key, nil
}

// getSecretForKeyID retrieves a specific secret key by ID (for verifying old entries).
func getSecretForKeyID(repoRoot, keyID string) ([]byte, error) {
	path, err := secretKeyPath(repoRoot)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rd := bufio.NewReader(f)
	for {
		line, truncated, err := readBoundedLine(rd, reviewCacheMaxSize)
		if len(line) > 0 && !truncated {
			line = bytes.TrimRight(line, "\r\n")
			var e secretKeyEntry
			if json.Unmarshal(line, &e) == nil && e.KeyID == keyID {
				return e.Key, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	return nil, fmt.Errorf("secret key %s not found", keyID)
}

// repoSecret returns the current HMAC secret for the repo.
// Loads or creates a persistent random key instead of deriving from path.
func repoSecret(repoRoot string) ([]byte, error) {
	_, key, err := loadOrCreateSecret(repoRoot)
	return key, err
}

// computeMAC computes HMAC-SHA256 of the cache entry data.
// Uses JSON serialization to avoid delimiter collision issues.
func computeMAC(secret []byte, e *reviewCacheEntry) string {
	// Create a struct without MAC for deterministic serialization
	data := struct {
		Key       string    `json:"key"`
		Findings  []Finding `json:"findings"`
		Timestamp time.Time `json:"ts"`
		KeyID     string    `json:"key_id,omitempty"`
	}{
		Key:       e.Key,
		Findings:  e.Findings,
		Timestamp: e.Timestamp,
		KeyID:     e.KeyID,
	}
	b, err := json.Marshal(data)
	if err != nil {
		// Should not happen with this structure
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// verifyMAC verifies the HMAC of a cache entry.
// Uses the KeyID to find the correct secret key for verification.
func verifyMAC(repoRoot string, e *reviewCacheEntry) bool {
	if e.MAC == "" {
		return false // entries without MAC are rejected
	}
	// Get the secret key for this entry's KeyID
	keyID := e.KeyID
	if keyID == "" {
		// Legacy entry without KeyID - try current secret
		secret, err := repoSecret(repoRoot)
		if err != nil {
			return false
		}
		expected := computeMAC(secret, e)
		return hmac.Equal([]byte(expected), []byte(e.MAC))
	}
	secret, err := getSecretForKeyID(repoRoot, keyID)
	if err != nil {
		return false
	}
	expected := computeMAC(secret, e)
	return hmac.Equal([]byte(expected), []byte(e.MAC))
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
// Entries with invalid or missing MAC are rejected (treated as corrupted).
func LoadReviewCache(path string, repoRoot string) ([]reviewCacheEntry, error) {
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
				if verifyMAC(repoRoot, &e) {
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

// SaveReviewCache writes entries to path atomically, creating parent dirs.
// Each entry is signed with HMAC using the per-repo secret.
func SaveReviewCache(path string, entries []reviewCacheEntry, repoRoot string) error {
	if path == "" {
		return nil
	}
	// Clone entries to avoid modifying caller's slice
	clone := append([]reviewCacheEntry(nil), entries...)
	keyID, secret, err := loadOrCreateSecret(repoRoot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Add MAC and KeyID to each entry
	for i := range clone {
		clone[i].KeyID = keyID
		clone[i].MAC = computeMAC(secret, &clone[i])
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
