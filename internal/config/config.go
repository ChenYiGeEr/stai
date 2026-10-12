// Package config loads stai's layered configuration: built-in defaults,
// environment overrides, a global file (~/.config/stai/config.toml) and an
// optional per-repo .stai.toml whose fields override everything else.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"stai/internal/git"
)

// Global holds process-wide settings. Lang drives both the CLI text and the
// AI output language; it is normalized (strict "zh-CN"/"en", invalid values
// fall back to zh-CN with a warning) by the caller after Load.
type Global struct {
	Lang string // "zh-CN" (default) or "en"
}

type Provider struct {
	BaseURL     string
	APIKey      string
	Model       string
	TimeoutSec  int     // HTTP timeout for one chat completion request
	Temperature float64 // request temperature; 0 = deterministic
}

type Commit struct {
	Style        string   // "conventional" or "free"
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
	SuggestFixes      bool     // ask the model for a concrete fix suggestion per finding
	Deterministic     bool     // master switch: make review deterministic via seed + cache (forces both on when true)
	Seed              bool     // send a deterministic seed to the model API
	Cache             bool     // cache review results keyed by diff+config
	ValidateLocations bool     // drop findings whose file/line is outside the diff
	Bundle            bool     // bundle related files into the same review group
	Rules             []string // project-specific rules injected into the review prompt
	// Optional provider overrides; empty fields inherit [provider]. Lets
	// review use a stronger model than gen while gen stays on a fast one.
	BaseURL     string
	APIKey      string
	Model       string
	Temperature *float64 // nil inherits provider.temperature (some models only accept 1)

	// RuleSets are path-scoped rule blocks from [[review.rule]] tables.
	// Rules listed here are ADDED to Rules for matching files; global Rules
	// always apply. See RulesForPath.
	RuleSets []ReviewRule
}

// ReviewLoop holds configuration for "stai review-loop-gen": the automated
// fix-review cycle that runs review, sends findings to an MCP client for
// fixing, applies the fix, and repeats until clean.
type ReviewLoop struct {
	MaxIterations int    // maximum review-fix iterations (0 = unlimited, capped at 100 internally)
	Timeout       int    // overall timeout in seconds (0 = no overall timeout)
	Strict        bool   // when true, LOW findings also count as failures
	FixPromptPath string // optional path to a custom fix prompt template file
	MCPTimeout    int    // per-fix-call timeout in seconds (0 = provider timeout)
	NoMCPFallback bool   // when true, fail if MCP client is unavailable instead of manual fallback
}

// ReviewRule is one [[review.rule]] block: when a file path matches any
// include pattern (and no exclude pattern), its rules are appended to the
// prompt for the review group containing that file.
type ReviewRule struct {
	Include []string // glob patterns; empty means "match every file"
	Exclude []string // glob patterns; a match skips this rule set
	Rules   []string // rules appended for matching files
}

// RulesForPath returns the deduplicated rules that apply to path: the
// global Rules always, plus the rules of every matching RuleSet. An invalid
// glob pattern in a rule set is reported as an error.
func (r Review) RulesForPath(p string) ([]string, error) {
	return MatchRules(r.Rules, r.RuleSets, p)
}

// MatchRules returns the deduplicated rules for path: the global rules
// always, plus the rules of every rule set whose globs match the path.
// It is exported so the ai package can apply the same matching when a
// review group is assembled from several files. An invalid glob pattern
// aborts with an error instead of silently not matching.
func MatchRules(rules []string, sets []ReviewRule, p string) ([]string, error) {
	out := make([]string, 0, len(rules)+4)
	out = append(out, rules...)
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s] = true
	}
	for _, rs := range sets {
		ok, err := rs.matches(p)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, rule := range rs.Rules {
			if !seen[rule] {
				seen[rule] = true
				out = append(out, rule)
			}
		}
	}
	return out, nil
}

// matches reports whether path satisfies this rule's include/exclude globs.
// A pattern without "/" also matches against the file's base name, so
// "*.go" matches "src/a.go". Matching uses slash-separated paths. An
// unparsable glob pattern is returned as an error.
func (rs ReviewRule) matches(p string) (bool, error) {
	if len(rs.Exclude) > 0 {
		for _, ex := range rs.Exclude {
			ok, err := globMatch(ex, p)
			if err != nil {
				return false, fmt.Errorf("review.rule exclude %q: %w", ex, err)
			}
			if ok {
				return false, nil
			}
		}
	}
	if len(rs.Include) == 0 {
		return true, nil
	}
	for _, inc := range rs.Include {
		ok, err := globMatch(inc, p)
		if err != nil {
			return false, fmt.Errorf("review.rule include %q: %w", inc, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// globMatch matches pattern against the full path and, when the pattern has
// no slash, against the base name too (so "*.go" matches "src/a.go").
// A malformed pattern is surfaced as an error rather than a silent no-match.
func globMatch(pattern, p string) (bool, error) {
	ok, err := path.Match(pattern, p)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(p))
		if err != nil {
			return false, err
		}
		return ok, nil
	}
	return false, nil
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
	PRTitleActionCaption          string // menu caption of the PR title custom action
	ExplainActionCaption          string // menu caption of the file-explain custom action
	SplitActionCaption            string // menu caption of the commit-split custom action
	ChangelogActionCaption        string // menu caption of the changelog custom action
}

type Log struct {
	Path string // diagnostic log file; "~/" is expanded
}

type Config struct {
	Global     Global
	Provider   Provider
	Commit     Commit
	Review     Review
	ReviewLoop ReviewLoop
	PrePush    PrePush
	Hook       Hook
	Notify     Notify
	SourceTree SourceTree
	Log        Log
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Global: Global{Lang: "zh-CN"},
		Provider: Provider{
			BaseURL:    "http://localhost:11434/v1", // local Ollama by default
			APIKey:     "",
			Model:      "qwen2.5-coder:7b",
			TimeoutSec: 120,
		},
		Commit: Commit{
			Style:        "conventional",
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
			SuggestFixes:      true,
			Deterministic:     true,
			Seed:              true,
			Cache:             true,
			ValidateLocations: true,
			Bundle:            true,
		},
		ReviewLoop: ReviewLoop{
			MaxIterations: 10,
			Timeout:       300, // 5 minutes
			Strict:        false,
			MCPTimeout:    30,
			NoMCPFallback: false,
		},
		PrePush: PrePush{
			BaseRef: "origin/main",
			Strict:  false,
		},
		Hook: Hook{TimeoutSec: 120},
		Notify: Notify{
			Title:    "stai",
			Subtitle: "", // empty = language-dependent default resolved at notify time
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
			PRTitleActionCaption:          "AI 生成 PR 标题",
			ExplainActionCaption:          "AI 解释选中文件",
			SplitActionCaption:            "AI 拆分 commit 建议",
			ChangelogActionCaption:        "AI 生成 changelog",
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
	if cfg.ReviewLoop.MaxIterations < 0 {
		return errors.New("review_loop.max_iterations must not be negative")
	}
	if cfg.ReviewLoop.Timeout < 0 {
		return errors.New("review_loop.timeout must not be negative")
	}
	if cfg.ReviewLoop.MCPTimeout < 0 {
		return errors.New("review_loop.mcp_timeout must not be negative")
	}
	if cfg.PrePush.BaseRef == "" {
		return errors.New("pre_push.base_ref must not be empty")
	}
	// Fail fast on unparsable [[review.rule]] globs instead of silently
	// never matching at review time.
	for _, rs := range cfg.Review.RuleSets {
		for _, pattern := range append(append([]string{}, rs.Include...), rs.Exclude...) {
			if _, err := path.Match(pattern, "probe"); err != nil {
				return fmt.Errorf("review.rule pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("STAI_LANG"); v != "" {
		cfg.Global.Lang = v
	}
	if v := os.Getenv("STAI_BASE_URL"); v != "" {
		cfg.Provider.BaseURL = v
	}
	if v := os.Getenv("STAI_API_KEY"); v != "" {
		cfg.Provider.APIKey = v
	}
	if v := os.Getenv("STAI_MODEL"); v != "" {
		cfg.Provider.Model = v
	}
	if v := os.Getenv("STAI_REVIEW_DETERMINISTIC"); v != "" {
		cfg.Review.Deterministic = v == "1" || strings.EqualFold(v, "true")
	}
	// Deterministic mode requires seed + cache; force them on unless an
	// explicit per-feature env var disables them below.
	if cfg.Review.Deterministic {
		cfg.Review.Seed = true
		cfg.Review.Cache = true
	}
	if v := os.Getenv("STAI_REVIEW_SEED"); v != "" {
		cfg.Review.Seed = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("STAI_REVIEW_CACHE"); v != "" {
		cfg.Review.Cache = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("STAI_NO_CACHE"); v != "" {
		cfg.Review.Cache = !(v == "1" || strings.EqualFold(v, "true"))
	}
	if v := os.Getenv("STAI_REVIEW_VALIDATE_LOCATIONS"); v != "" {
		cfg.Review.ValidateLocations = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("STAI_REVIEW_BUNDLE"); v != "" {
		cfg.Review.Bundle = v == "1" || strings.EqualFold(v, "true")
	}
}

// loadFile parses a minimal TOML subset: [section] headers, [[review.rule]]
// array-of-table headers, scalar key = value pairs (strings, integers,
// booleans), single-line arrays of strings, # comments. That covers every
// config stai defines without pulling in a TOML dependency.
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
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			section = strings.TrimSpace(line[2 : len(line)-2])
			if section != "review.rule" {
				return fmt.Errorf("%s:%d: unsupported table [[%s]] (only [[review.rule]])", path, lineNo, section)
			}
			cfg.Review.RuleSets = append(cfg.Review.RuleSets, ReviewRule{})
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
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if section == "review.rule" {
			if len(cfg.Review.RuleSets) == 0 {
				return fmt.Errorf("%s:%d: key %q before any [[review.rule]] header", path, lineNo, key)
			}
			if err := setRule(&cfg.Review.RuleSets[len(cfg.Review.RuleSets)-1], key, val); err != nil {
				return fmt.Errorf("%s:%d: %w", path, lineNo, err)
			}
			continue
		}
		if err := set(cfg, section, key, val); err != nil {
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
	case "global.lang":
		err = setString(raw, &cfg.Global.Lang)
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
	case "review.suggest_fixes":
		err = setBool(raw, &cfg.Review.SuggestFixes)
	case "review.deterministic":
		err = setBool(raw, &cfg.Review.Deterministic)
	case "review.seed":
		err = setBool(raw, &cfg.Review.Seed)
	case "review.cache":
		err = setBool(raw, &cfg.Review.Cache)
	case "review.validate_locations":
		err = setBool(raw, &cfg.Review.ValidateLocations)
	case "review.bundle":
		err = setBool(raw, &cfg.Review.Bundle)
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
	case "review_loop.max_iterations":
		err = setInt(raw, &cfg.ReviewLoop.MaxIterations)
	case "review_loop.timeout":
		err = setInt(raw, &cfg.ReviewLoop.Timeout)
	case "review_loop.strict":
		err = setBool(raw, &cfg.ReviewLoop.Strict)
	case "review_loop.fix_prompt_path":
		err = setString(raw, &cfg.ReviewLoop.FixPromptPath)
	case "review_loop.mcp_timeout":
		err = setInt(raw, &cfg.ReviewLoop.MCPTimeout)
	case "review_loop.no_mcp_fallback":
		err = setBool(raw, &cfg.ReviewLoop.NoMCPFallback)
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
	case "sourcetree.pr_title_action_caption":
		err = setString(raw, &cfg.SourceTree.PRTitleActionCaption)
	case "sourcetree.explain_action_caption":
		err = setString(raw, &cfg.SourceTree.ExplainActionCaption)
	case "sourcetree.split_action_caption":
		err = setString(raw, &cfg.SourceTree.SplitActionCaption)
	case "sourcetree.changelog_action_caption":
		err = setString(raw, &cfg.SourceTree.ChangelogActionCaption)
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

// setRule parses one key inside a [[review.rule]] table.
func setRule(rs *ReviewRule, key, raw string) error {
	var err error
	switch key {
	case "include":
		err = setStrings(raw, &rs.Include)
	case "exclude":
		err = setStrings(raw, &rs.Exclude)
	case "rules":
		err = setStrings(raw, &rs.Rules)
	default:
		return fmt.Errorf("unknown key %q in [[review.rule]]", key)
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

// ExpandHome replaces a leading "~/" with the user's home directory.
// It is the exported form used by commands that delete log files and the like.
func ExpandHome(p string) string { return expandHome(p) }

// GlobalPath returns the path to the global configuration file.
func GlobalPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "stai", "config.toml")
}

// BackupGlobal copies the existing global config file to path.bak.
// If the file does not exist, it returns nil without doing anything.
func BackupGlobal() error {
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".bak", data, 0o600); err != nil {
		return err
	}
	// os.WriteFile does not change the mode of an existing file; tighten it
	// explicitly so backups never leak wider permissions.
	return os.Chmod(path+".bak", 0o600)
}

// WriteGlobal writes a fresh, fully-commented config file for the user.
// Only the values collected by the install wizard are uncommented; every
// other key is present as a comment so the file doubles as documentation.
func WriteGlobal(cfg Config, lang string) error {
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var tmpl string
	if lang == "en" {
		tmpl = configTemplateEN
	} else {
		tmpl = configTemplateZH
	}
	content := fmt.Sprintf(tmpl,
		cfg.Global.Lang,
		cfg.Provider.BaseURL,
		cfg.Provider.APIKey,
		cfg.Provider.Model,
	)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	// os.WriteFile does not change the mode of an existing file; tighten it
	// explicitly so an insecure pre-existing config is hardened on rewrite.
	return os.Chmod(path, 0o600)
}

// FileInsecure reports whether the global config file exists and has
// permission bits beyond the owner (i.e. group or others can read it).
// It returns false when the file does not exist or the home directory
// cannot be determined.
func FileInsecure() (bool, os.FileMode) {
	path := GlobalPath()
	if path == "" {
		return false, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, 0
	}
	perm := info.Mode().Perm()
	return perm&0o077 != 0, perm
}

const configTemplateZH = `# stai 配置文件
# 生效顺序(低到高): 内置默认值 < 本文件 < 仓库级 .stai.toml < 环境变量
# 文档: 见 README.md / README.en.md

[global]
# 界面语言与 AI 输出语言: zh-CN 或 en
lang = "%s"

[provider]
# OpenAI 兼容 API 地址,例如 Ollama 默认 http://localhost:11434/v1
base_url = %q
api_key = %q
# 模型名,例如 qwen2.5-coder:7b
model = %q
# 单次请求超时(秒)
# timeout_seconds = 120
# 采样温度;0 表示确定性,部分模型只接受 1
# temperature = 0

[commit]
# 提交信息风格,目前仅支持 conventional
# style = "conventional"
# 允许的 Conventional Commits type
# types = ["feat", "fix", "refactor", "docs", "chore", "test", "style", "perf", "build", "ci"]
# subject 长度建议
# subject_max = 50
# 超过多少行才请求 body
# body_min_lines = 100
# 超过多少个文件才请求 body
# body_min_files = 3
# body 最多条目数
# body_max_items = 5
# diff 截断长度(字节)
# max_diff_chars = 60000
# 输出不合规时的重试次数
# retries = 1

[review]
# strict = true 时,pre-commit/pre-push 发现高危问题会阻断
# strict = false
# 超过多少条问题时,通知改为报告文件路径
# notify_max_findings = 5
# 报告文件路径(相对仓库根)
# report_path = ".git/stai-review.md"
# 按文件分组审查,每组最大变更行数
# group_max_lines = 100
# 并行审查组数
# concurrency = 4
# 是否要求模型给出具体修改建议
# suggest_fixes = true
# 是否让 review 结果可复现:发送确定性 seed 并缓存结果
# deterministic = true
# 是否在请求中发送 seed(需 provider 支持)
# seed = true
# 是否启用本地结果缓存
# cache = true
# 是否校验审查结果中的文件/行号确实出现在本次 diff 中,丢弃漂移的 location
# validate_locations = true
# 是否把相关文件(同目录/本地化后缀/测试文件等)优先分到同一审查组
# bundle = true
# 项目自定义审查规则(字符串数组)
# rules = []
# 按路径追加规则;include 命中的文件在全局 rules 之外额外收到这些规则
# [[review.rule]]
# include = ["*.go"]
# exclude = ["*_test.go"]
# rules = ["检查 err 是否被正确处理"]
# 以下三项若设置,则 review 使用独立模型/地址/密钥
# base_url = ""
# api_key = ""
# model = ""
# 独立采样温度,不填则继承 provider.temperature
# temperature = 0.7

[review_loop]
# 最大审查-修复迭代次数(0 = 无上限,内部封顶 100)
# max_iterations = 10
# 整体超时(秒),0 表示无整体超时
# timeout = 300
# strict = true 时,LOW 级别问题也计为失败
# strict = false
# 自定义修复 prompt 模板文件路径
# fix_prompt_path = ""
# 单次 MCP 修复调用超时(秒),0 表示继承 provider.timeout_seconds
# mcp_timeout = 30
# 禁用 MCP 不可用时的人工降级模式
# no_mcp_fallback = false

[pre_push]
# 分支审查的对比基准
# base_ref = "origin/main"
# strict = true 时,push 前发现高危问题会阻断
# strict = false

[hook]
# prepare-commit-msg 钩子超时(秒)
# timeout_seconds = 120

[notify]
# 通知标题
# title = "stai"
# 通知副标题;空则使用语言默认文案
# subtitle = ""

[sourcetree]
# SourceTree 自定义动作菜单文案与快捷键
# action_caption = "AI 生成提交信息"
# shortcut_key_code = 5
# shortcut_modifiers = 524288
# shortcut_display = "⌥G"
# review_action_caption = "AI 审查改动"
# review_shortcut_key_code = 15
# review_shortcut_modifiers = 524288
# review_shortcut_display = "⌥R"
# pr_action_caption = "AI 生成 PR 描述"
# pr_shortcut_key_code = 35
# pr_shortcut_modifiers = 524288
# pr_shortcut_display = "⌥P"
# review_branch_action_caption = "AI 审查分支"
# review_branch_shortcut_key_code = 0
# review_branch_shortcut_modifiers = 0
# review_branch_shortcut_display = ""
# stash_msg_action_caption = "AI 生成 stash 信息"
# stash_msg_shortcut_key_code = 0
# stash_msg_shortcut_modifiers = 0
# stash_msg_shortcut_display = ""
# pr_title_action_caption = "AI 生成 PR 标题"
# explain_action_caption = "AI 解释选中文件"
# split_action_caption = "AI 拆分 commit 建议"
# changelog_action_caption = "AI 生成 changelog"

[log]
# 诊断日志路径,~ 会自动展开
# path = "~/Library/Logs/stai.log"
`

const configTemplateEN = `# stai configuration file
# Precedence (lowest to highest): built-in defaults < this file < repo .stai.toml < env vars
# Docs: see README.md / README.en.md

[global]
# UI and AI output language: zh-CN or en
lang = "%s"

[provider]
# OpenAI-compatible API endpoint, e.g. http://localhost:11434/v1 for Ollama
base_url = %q
api_key = %q
# Model name, e.g. qwen2.5-coder:7b
model = %q
# Timeout for a single request (seconds)
# timeout_seconds = 120
# Sampling temperature; 0 is deterministic, some models only accept 1
# temperature = 0

[commit]
# Commit message style; only "conventional" is supported
# style = "conventional"
# Allowed Conventional Commits types
# types = ["feat", "fix", "refactor", "docs", "chore", "test", "style", "perf", "build", "ci"]
# Subject length hint
# subject_max = 50
# Request a body only when changed lines exceed this
# body_min_lines = 100
# ... or when more than this many files are touched
# body_min_files = 3
# Maximum body bullet items
# body_max_items = 5
# Diff truncation limit (bytes)
# max_diff_chars = 60000
# Retry count when output violates the rules
# retries = 1

[review]
# When strict = true, pre-commit/pre-push blocks on high-severity findings
# strict = false
# Findings beyond this are sent to the report file instead of the notification
# notify_max_findings = 5
# Report file path, relative to the repo root
# report_path = ".git/stai-review.md"
# Diff is split into groups; each group's changed lines stay under this
# group_max_lines = 100
# Number of groups reviewed in parallel
# concurrency = 4
# Ask the model for a concrete fix suggestion per finding
# suggest_fixes = true
# Make review results reproducible: send a deterministic seed and cache results
# deterministic = true
# Send a seed in the request (provider support required)
# seed = true
# Enable local result cache
# cache = true
# Drop findings whose file/line does not appear in this diff
# validate_locations = true
# Bundle related files (same dir/locale suffix/test files) into one group
# bundle = true
# Project-specific review rules (array of strings)
# rules = []
# Path-scoped rules; matching files get these on top of the global rules
# [[review.rule]]
# include = ["*.go"]
# exclude = ["*_test.go"]
# rules = ["check that errors are handled correctly"]
# If set, review uses a separate endpoint/key/model
# base_url = ""
# api_key = ""
# model = ""
# Separate sampling temperature; empty inherits provider.temperature
# temperature = 0.7

[review_loop]
# Maximum review-fix iterations (0 = unlimited, internally capped at 100)
# max_iterations = 10
# Overall timeout in seconds (0 = no overall timeout)
# timeout = 300
# When strict = true, LOW findings also count as failures
# strict = false
# Custom fix prompt template file path
# fix_prompt_path = ""
# Per-fix-call timeout in seconds (0 = provider timeout)
# mcp_timeout = 30
# Disable manual fallback when MCP client is unavailable
# no_mcp_fallback = false

[pre_push]
# Base ref for branch reviews
# base_ref = "origin/main"
# When strict = true, push is blocked on high-severity findings
# strict = false

[hook]
# prepare-commit-msg hook timeout (seconds)
# timeout_seconds = 120

[notify]
# Notification title
# title = "stai"
# Notification subtitle; empty means use the language default
# subtitle = ""

[sourcetree]
# SourceTree custom-action captions and shortcuts
# action_caption = "AI 生成提交信息"
# shortcut_key_code = 5
# shortcut_modifiers = 524288
# shortcut_display = "⌥G"
# review_action_caption = "AI 审查改动"
# review_shortcut_key_code = 15
# review_shortcut_modifiers = 524288
# review_shortcut_display = "⌥R"
# pr_action_caption = "AI 生成 PR 描述"
# pr_shortcut_key_code = 35
# pr_shortcut_modifiers = 524288
# pr_shortcut_display = "⌥P"
# review_branch_action_caption = "AI 审查分支"
# review_branch_shortcut_key_code = 0
# review_branch_shortcut_modifiers = 0
# review_branch_shortcut_display = ""
# stash_msg_action_caption = "AI 生成 stash 信息"
# stash_msg_shortcut_key_code = 0
# stash_msg_shortcut_modifiers = 0
# stash_msg_shortcut_display = ""
# pr_title_action_caption = "AI 生成 PR 标题"
# explain_action_caption = "AI 解释选中文件"
# split_action_caption = "AI 拆分 commit 建议"
# changelog_action_caption = "AI 生成 changelog"

[log]
# Diagnostic log path; ~/ is expanded automatically
# path = "~/Library/Logs/stai.log"
`

// parseString handles "double-quoted strings" and 'single-quoted' strings.
func parseString(raw string) (string, error) {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			return raw[1 : len(raw)-1], nil
		}
	}
	return "", fmt.Errorf("unsupported value %q (use a quoted string)", raw)
}
