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
	if cfg.PrePush.BaseRef == "" {
		return errors.New("pre_push.base_ref must not be empty")
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
	return os.WriteFile(path, []byte(content), 0o600)
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
# 项目自定义审查规则(字符串数组)
# rules = []
# 以下三项若设置,则 review 使用独立模型/地址/密钥
# base_url = ""
# api_key = ""
# model = ""
# 独立采样温度,不填则继承 provider.temperature
# temperature = 0.7

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
# Project-specific review rules (array of strings)
# rules = []
# If set, review uses a separate endpoint/key/model
# base_url = ""
# api_key = ""
# model = ""
# Separate sampling temperature; empty inherits provider.temperature
# temperature = 0.7

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
