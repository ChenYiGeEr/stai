// Package config loads stai's layered configuration: built-in defaults,
// environment overrides, a global file (~/.config/stai/config.toml) and an
// optional per-repo .stai.toml whose fields override everything else.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"stai/internal/git"
)

type Provider struct {
	BaseURL     string
	APIKey      string
	Model       string
	TimeoutSec  int     // HTTP timeout for one chat completion request
	Temperature float64 // request temperature; 0 = deterministic
}

type Commit struct {
	Style        string   // "conventional" or "free"
	Language     string   // e.g. "zh-CN"
	Types        []string // allowed Conventional Commits types
	SubjectMax   int      // subject length hint (characters) given to the model
	BodyMinLines int      // a body is requested only when changed lines exceed this
	BodyMinFiles int      // ... or when touched files exceed this
	BodyMaxItems int      // maximum bullet points in a body
	MaxDiffChars int      // diff is truncated beyond this many bytes
	Retries      int      // validation retries after a non-conforming reply
}

type Review struct {
	Strict            bool     // true: pre-commit blocks on high-severity findings
	NotifyMaxFindings int      // findings beyond this go to the report file, not the notification
	ReportPath        string   // full report location, relative to the repo root
	GroupMaxLines     int      // diff is reviewed in per-file groups whose changed lines stay under this
	Concurrency       int      // groups reviewed in parallel; 1 = serial
	Rules             []string // project-specific rules injected into the review prompt
	// Optional provider overrides; empty fields inherit [provider]. Lets
	// review use a stronger model than gen while gen stays on a fast one.
	BaseURL     string
	APIKey      string
	Model       string
	Temperature *float64 // nil inherits provider.temperature (some models only accept 1)
}

type PrePush struct {
	BaseRef string // ref to compare against, e.g. "origin/main"
	Strict  bool   // true: pre-push hook blocks push on high-severity findings
}

type Hook struct {
	TimeoutSec int // upper bound for the prepare-commit-msg hook
}

type Notify struct {
	Title    string
	Subtitle string
}

type SourceTree struct {
	ActionCaption                 string // menu caption of the gen custom action
	ShortcutKeyCode               int    // kVK code of the gen shortcut key (5 = G)
	ShortcutModifiers             int    // NSEvent modifier flags (524288 = Option)
	ShortcutDisplay               string // gen shortcut text shown in SourceTree
	ReviewActionCaption           string // menu caption of the review custom action
	ReviewShortcutKeyCode         int    // kVK code of the review shortcut key (15 = R)
	ReviewShortcutModifiers       int    // NSEvent modifier flags (524288 = Option)
	ReviewShortcutDisplay         string // review shortcut text shown in SourceTree
	PRActionCaption               string // menu caption of the PR description custom action
	PRShortcutKeyCode             int    // kVK code of the pr shortcut key (35 = P)
	PRShortcutModifiers           int    // NSEvent modifier flags (524288 = Option)
	PRShortcutDisplay             string // pr shortcut text shown in SourceTree
	ReviewBranchActionCaption     string // menu caption of the branch review custom action
	ReviewBranchShortcutKeyCode   int    // no shortcut by default (0)
	ReviewBranchShortcutModifiers int    // no shortcut by default (0)
	ReviewBranchShortcutDisplay   string // empty when no shortcut
	StashMsgActionCaption         string // menu caption of the stash message custom action
	StashMsgShortcutKeyCode       int    // no shortcut by default (0)
	StashMsgShortcutModifiers     int    // no shortcut by default (0)
	StashMsgShortcutDisplay       string // empty when no shortcut
}

type Log struct {
	Path string // diagnostic log file; "~/" is expanded
}

type Config struct {
	Provider   Provider
	Commit     Commit
	Review     Review
	PrePush    PrePush
	Hook       Hook
	Notify     Notify
	SourceTree SourceTree
	Log        Log
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Provider: Provider{
			BaseURL:    "http://localhost:11434/v1", // local Ollama by default
			APIKey:     "",
			Model:      "qwen2.5-coder:7b",
			TimeoutSec: 120,
		},
		Commit: Commit{
			Style:        "conventional",
			Language:     "zh-CN",
			Types:        []string{"feat", "fix", "refactor", "docs", "chore", "test", "style", "perf", "build", "ci"},
			SubjectMax:   50,
			BodyMinLines: 100,
			BodyMinFiles: 3,
			BodyMaxItems: 5,
			MaxDiffChars: 60000,
			Retries:      1,
		},
		Review: Review{
			Strict:            false,
			NotifyMaxFindings: 5,
			ReportPath:        ".git/stai-review.md",
			GroupMaxLines:     100,
			Concurrency:       4,
		},
		PrePush: PrePush{
			BaseRef: "origin/main",
			Strict:  false,
		},
		Hook: Hook{TimeoutSec: 120},
		Notify: Notify{
			Title:    "stai",
			Subtitle: "提交信息已复制到剪贴板，Cmd+V 粘贴到提交框",
		},
		SourceTree: SourceTree{
			ActionCaption:                 "AI 生成提交信息",
			ShortcutKeyCode:               5,
			ShortcutModifiers:             524288,
			ShortcutDisplay:               "⌥G",
			ReviewActionCaption:           "AI 审查改动",
			ReviewShortcutKeyCode:         15, // kVK_ANSI_R
			ReviewShortcutModifiers:       524288,
			ReviewShortcutDisplay:         "⌥R",
			PRActionCaption:               "AI 生成 PR 描述",
			PRShortcutKeyCode:             35, // kVK_ANSI_P
			PRShortcutModifiers:           524288,
			PRShortcutDisplay:             "⌥P",
			ReviewBranchActionCaption:     "AI 审查分支",
			ReviewBranchShortcutKeyCode:   0,
			ReviewBranchShortcutModifiers: 0,
			ReviewBranchShortcutDisplay:   "",
			StashMsgActionCaption:         "AI 生成 stash 信息",
			StashMsgShortcutKeyCode:       0,
			StashMsgShortcutModifiers:     0,
			StashMsgShortcutDisplay:       "",
		},
		Log: Log{Path: filepath.Join(home, "Library", "Logs", "stai.log")},
	}
}

// Load builds the effective configuration for the current repository.
// Precedence (lowest to highest): defaults, global file, repo file, env.
func Load() (Config, error) {
	cfg := Default()

	home, err := os.UserHomeDir()
	if err == nil {
		if err := loadFile(&cfg, filepath.Join(home, ".config", "stai", "config.toml")); err != nil && !os.IsNotExist(err) {
			return cfg, fmt.Errorf("global config: %w", err)
		}
	}

	if root, err := git.RepoRoot(); err == nil {
		if err := loadFile(&cfg, filepath.Join(root, ".stai.toml")); err != nil && !os.IsNotExist(err) {
			return cfg, fmt.Errorf("repo config: %w", err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if cfg.Commit.Style != "conventional" {
		return fmt.Errorf("commit.style %q is not supported (only \"conventional\")", cfg.Commit.Style)
	}
	if len(cfg.Commit.Types) == 0 {
		return errors.New("commit.types must not be empty")
	}
	if cfg.Provider.TimeoutSec <= 0 || cfg.Hook.TimeoutSec <= 0 {
		return errors.New("timeout_seconds must be positive")
	}
	if cfg.Commit.Retries < 0 || cfg.Commit.MaxDiffChars < 0 {
		return errors.New("commit.retries and commit.max_diff_chars must not be negative")
	}
	if cfg.Provider.Temperature < 0 || (cfg.Review.Temperature != nil && *cfg.Review.Temperature < 0) {
		return errors.New("temperature must not be negative")
	}
	if cfg.Review.Concurrency < 1 {
		return errors.New("review.concurrency must be at least 1")
	}
	if cfg.PrePush.BaseRef == "" {
		return errors.New("pre_push.base_ref must not be empty")
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("STAI_BASE_URL"); v != "" {
		cfg.Provider.BaseURL = v
	}
	if v := os.Getenv("STAI_API_KEY"); v != "" {
		cfg.Provider.APIKey = v
	}
	if v := os.Getenv("STAI_MODEL"); v != "" {
		cfg.Provider.Model = v
	}
}

// loadFile parses a minimal TOML subset: [section] headers, scalar
// key = value pairs (strings, integers, booleans), single-line arrays of
// strings, # comments. That covers every config stai defines without
// pulling in a TOML dependency.
func loadFile(cfg *Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	section := ""
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		line = stripComment(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected key = value", path, lineNo)
		}
		if err := set(cfg, section, strings.TrimSpace(key), strings.TrimSpace(val)); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	return sc.Err()
}

// stripComment removes a trailing # comment, leaving # inside quoted strings
// untouched (URLs and messages may legitimately contain it).
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '#':
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func set(cfg *Config, section, key, raw string) error {
	var err error
	switch section + "." + key {
	case "provider.base_url":
		err = setString(raw, &cfg.Provider.BaseURL)
	case "provider.api_key":
		err = setString(raw, &cfg.Provider.APIKey)
	case "provider.model":
		err = setString(raw, &cfg.Provider.Model)
	case "provider.timeout_seconds":
		err = setInt(raw, &cfg.Provider.TimeoutSec)
	case "provider.temperature":
		err = setFloat(raw, &cfg.Provider.Temperature)
	case "commit.style":
		err = setString(raw, &cfg.Commit.Style)
	case "commit.language":
		err = setString(raw, &cfg.Commit.Language)
	case "commit.types":
		err = setStrings(raw, &cfg.Commit.Types)
	case "commit.subject_max":
		err = setInt(raw, &cfg.Commit.SubjectMax)
	case "commit.body_min_lines":
		err = setInt(raw, &cfg.Commit.BodyMinLines)
	case "commit.body_min_files":
		err = setInt(raw, &cfg.Commit.BodyMinFiles)
	case "commit.body_max_items":
		err = setInt(raw, &cfg.Commit.BodyMaxItems)
	case "commit.max_diff_chars":
		err = setInt(raw, &cfg.Commit.MaxDiffChars)
	case "commit.retries":
		err = setInt(raw, &cfg.Commit.Retries)
	case "review.strict":
		err = setBool(raw, &cfg.Review.Strict)
	case "review.notify_max_findings":
		err = setInt(raw, &cfg.Review.NotifyMaxFindings)
	case "review.report_path":
		err = setString(raw, &cfg.Review.ReportPath)
	case "review.group_max_lines":
		err = setInt(raw, &cfg.Review.GroupMaxLines)
	case "review.concurrency":
		err = setInt(raw, &cfg.Review.Concurrency)
	case "review.rules":
		err = setStrings(raw, &cfg.Review.Rules)
	case "review.base_url":
		err = setString(raw, &cfg.Review.BaseURL)
	case "review.api_key":
		err = setString(raw, &cfg.Review.APIKey)
	case "review.model":
		err = setString(raw, &cfg.Review.Model)
	case "review.temperature":
		err = setFloatPtr(raw, &cfg.Review.Temperature)
	case "pre_push.base_ref":
		err = setString(raw, &cfg.PrePush.BaseRef)
	case "pre_push.strict":
		err = setBool(raw, &cfg.PrePush.Strict)
	case "hook.timeout_seconds":
		err = setInt(raw, &cfg.Hook.TimeoutSec)
	case "notify.title":
		err = setString(raw, &cfg.Notify.Title)
	case "notify.subtitle":
		err = setString(raw, &cfg.Notify.Subtitle)
	case "sourcetree.action_caption":
		err = setString(raw, &cfg.SourceTree.ActionCaption)
	case "sourcetree.shortcut_key_code":
		err = setInt(raw, &cfg.SourceTree.ShortcutKeyCode)
	case "sourcetree.shortcut_modifiers":
		err = setInt(raw, &cfg.SourceTree.ShortcutModifiers)
	case "sourcetree.shortcut_display":
		err = setString(raw, &cfg.SourceTree.ShortcutDisplay)
	case "sourcetree.review_action_caption":
		err = setString(raw, &cfg.SourceTree.ReviewActionCaption)
	case "sourcetree.review_shortcut_key_code":
		err = setInt(raw, &cfg.SourceTree.ReviewShortcutKeyCode)
	case "sourcetree.review_shortcut_modifiers":
		err = setInt(raw, &cfg.SourceTree.ReviewShortcutModifiers)
	case "sourcetree.review_shortcut_display":
		err = setString(raw, &cfg.SourceTree.ReviewShortcutDisplay)
	case "sourcetree.pr_action_caption":
		err = setString(raw, &cfg.SourceTree.PRActionCaption)
	case "sourcetree.pr_shortcut_key_code":
		err = setInt(raw, &cfg.SourceTree.PRShortcutKeyCode)
	case "sourcetree.pr_shortcut_modifiers":
		err = setInt(raw, &cfg.SourceTree.PRShortcutModifiers)
	case "sourcetree.pr_shortcut_display":
		err = setString(raw, &cfg.SourceTree.PRShortcutDisplay)
	case "sourcetree.review_branch_action_caption":
		err = setString(raw, &cfg.SourceTree.ReviewBranchActionCaption)
	case "sourcetree.review_branch_shortcut_key_code":
		err = setInt(raw, &cfg.SourceTree.ReviewBranchShortcutKeyCode)
	case "sourcetree.review_branch_shortcut_modifiers":
		err = setInt(raw, &cfg.SourceTree.ReviewBranchShortcutModifiers)
	case "sourcetree.review_branch_shortcut_display":
		err = setString(raw, &cfg.SourceTree.ReviewBranchShortcutDisplay)
	case "sourcetree.stash_msg_action_caption":
		err = setString(raw, &cfg.SourceTree.StashMsgActionCaption)
	case "sourcetree.stash_msg_shortcut_key_code":
		err = setInt(raw, &cfg.SourceTree.StashMsgShortcutKeyCode)
	case "sourcetree.stash_msg_shortcut_modifiers":
		err = setInt(raw, &cfg.SourceTree.StashMsgShortcutModifiers)
	case "sourcetree.stash_msg_shortcut_display":
		err = setString(raw, &cfg.SourceTree.StashMsgShortcutDisplay)
	case "log.path":
		var p string
		if err = setString(raw, &p); err == nil {
			cfg.Log.Path = expandHome(p)
		}
	default:
		return fmt.Errorf("unknown key %q in section [%s]", key, section)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func setString(raw string, dst *string) error {
	v, err := parseString(raw)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func setBool(raw string, dst *bool) error {
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	*dst = b
	return nil
}

func setInt(raw string, dst *int) error {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("expected an integer, got %q", raw)
	}
	*dst = n
	return nil
}

func setFloat(raw string, dst *float64) error {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("expected a number, got %q", raw)
	}
	*dst = f
	return nil
}

// setFloatPtr parses a number into a *float64, allocating on first set.
func setFloatPtr(raw string, dst **float64) error {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("expected a number, got %q", raw)
	}
	*dst = &f
	return nil
}

// setStrings parses a single-line array such as ["feat", "fix"].
func setStrings(raw string, dst *[]string) error {
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return fmt.Errorf("expected an array like [\"a\", \"b\"], got %q", raw)
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	out := []string{}
	if inner != "" {
		for _, item := range strings.Split(inner, ",") {
			v, err := parseString(strings.TrimSpace(item))
			if err != nil {
				return err
			}
			out = append(out, v)
		}
	}
	*dst = out
	return nil
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// parseString handles "double-quoted strings" and 'single-quoted' strings.
func parseString(raw string) (string, error) {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			return raw[1 : len(raw)-1], nil
		}
	}
	return "", fmt.Errorf("unsupported value %q (use a quoted string)", raw)
}
