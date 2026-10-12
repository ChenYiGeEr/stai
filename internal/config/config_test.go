package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileParsesSubset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
# global settings
[global]
lang = "en"

[provider]
base_url = "http://192.168.1.10:1234/v1"
api_key  = 'sk-test'
model    = "deepseek-coder"

[commit]
style    = "free"

[review]
strict = true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := loadFile(&cfg, path); err != nil {
		t.Fatal(err)
	}
	if cfg.Global.Lang != "en" {
		t.Errorf("global.lang not parsed: %+v", cfg.Global)
	}
	if cfg.Provider.BaseURL != "http://192.168.1.10:1234/v1" ||
		cfg.Provider.APIKey != "sk-test" ||
		cfg.Provider.Model != "deepseek-coder" {
		t.Errorf("provider not parsed: %+v", cfg.Provider)
	}
	if cfg.Commit.Style != "free" {
		t.Errorf("commit not parsed: %+v", cfg.Commit)
	}
	if !cfg.Review.Strict {
		t.Errorf("review.strict not parsed")
	}
}

func TestLoadFileRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("[commit]\nbanana = \"yes\"\n"), 0o644)

	cfg := Default()
	if err := loadFile(&cfg, path); err == nil {
		t.Errorf("expected error for unknown key")
	}
}

func TestLoadFileStripsTrailingComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[provider]
base_url = "http://localhost:11434/v1"   # 默认本机 Ollama
api_key  = ""                             # 本地模型留空
model    = "qwen2.5-coder:7b"             # 含 # 的字符串要保留 http://x/#y

[review]
strict = false                            # trailing comment
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := loadFile(&cfg, path); err != nil {
		t.Fatalf("trailing comments must be accepted: %v", err)
	}
	if cfg.Provider.BaseURL != "http://localhost:11434/v1" || cfg.Provider.Model != "qwen2.5-coder:7b" {
		t.Errorf("values with trailing comments mis-parsed: %+v", cfg.Provider)
	}
	if cfg.Review.Strict {
		t.Errorf("strict should stay false")
	}
}

func TestLoadFileParsesNewKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[commit]
types = ["feat", "fix"]   # 只允许两种
subject_max = 72
max_diff_chars = 8000
retries = 2

[provider]
timeout_seconds = 30

[log]
path = "~/logs/stai.log"

[sourcetree]
action_caption = "AI 提交"
shortcut_key_code = 11
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := loadFile(&cfg, path); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Commit.Types; len(got) != 2 || got[0] != "feat" || got[1] != "fix" {
		t.Errorf("types = %v", got)
	}
	if cfg.Commit.SubjectMax != 72 || cfg.Commit.MaxDiffChars != 8000 || cfg.Commit.Retries != 2 {
		t.Errorf("commit ints not parsed: %+v", cfg.Commit)
	}
	if cfg.Provider.TimeoutSec != 30 {
		t.Errorf("provider.timeout_seconds = %d", cfg.Provider.TimeoutSec)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "logs", "stai.log"); cfg.Log.Path != want {
		t.Errorf("log.path = %q, want %q", cfg.Log.Path, want)
	}
	if cfg.SourceTree.ActionCaption != "AI 提交" || cfg.SourceTree.ShortcutKeyCode != 11 {
		t.Errorf("sourcetree not parsed: %+v", cfg.SourceTree)
	}
}

func TestLoadFileRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"int not a number": "[commit]\nsubject_max = \"fifty\"\n",
		"array not array":  "[commit]\ntypes = \"feat\"\n",
		"array item bare":  "[commit]\ntypes = [feat]\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			os.WriteFile(path, []byte(content), 0o644)
			cfg := Default()
			if err := loadFile(&cfg, path); err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestLoadFileParsesReviewAndSourceTreeKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[review]
strict = true
notify_max_findings = 3
report_path = "/tmp/stai-review.md"
group_max_lines = 42
concurrency = 2
suggest_fixes = false
rules = ["错误必须 logf", "禁止全局可变状态"]
base_url = "http://example.test/v1"
model = "review-model"
temperature = 1.5

[sourcetree]
review_action_caption = "AI 审查"
review_shortcut_key_code = 15
review_shortcut_modifiers = 524288
review_shortcut_display = "⌥R"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := loadFile(&cfg, path); err != nil {
		t.Fatal(err)
	}
	if !cfg.Review.Strict || cfg.Review.NotifyMaxFindings != 3 || cfg.Review.ReportPath != "/tmp/stai-review.md" ||
		cfg.Review.GroupMaxLines != 42 || cfg.Review.Concurrency != 2 || cfg.Review.SuggestFixes != false ||
		len(cfg.Review.Rules) != 2 || cfg.Review.Rules[0] != "错误必须 logf" ||
		cfg.Review.BaseURL != "http://example.test/v1" || cfg.Review.APIKey != "" || cfg.Review.Model != "review-model" {
		t.Errorf("review not parsed: %+v", cfg.Review)
	}
	if d := Default(); d.Review.Concurrency != 4 || !d.Review.SuggestFixes {
		t.Errorf("review defaults wrong: %+v", d.Review)
	}
	if cfg.Review.Temperature == nil || *cfg.Review.Temperature != 1.5 {
		t.Errorf("review.temperature not parsed: %+v", cfg.Review.Temperature)
	}
	if cfg.Provider.Temperature != 0 {
		t.Errorf("provider.temperature default = %v, want 0", cfg.Provider.Temperature)
	}
	if cfg.SourceTree.ReviewActionCaption != "AI 审查" || cfg.SourceTree.ReviewShortcutKeyCode != 15 ||
		cfg.SourceTree.ReviewShortcutModifiers != 524288 || cfg.SourceTree.ReviewShortcutDisplay != "⌥R" {
		t.Errorf("sourcetree review keys not parsed: %+v", cfg.SourceTree)
	}
}

func TestLoadFileParsesPrePushAndNewSourceTreeKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
[pre_push]
base_ref = "origin/develop"
strict = true

[sourcetree]
pr_action_caption = "AI PR"
pr_shortcut_key_code = 35
pr_shortcut_modifiers = 524288
pr_shortcut_display = "⌥P"
review_branch_action_caption = "AI 审分支"
stash_msg_action_caption = "AI stash"
pr_title_action_caption = "AI PR 标题"
explain_action_caption = "AI 解释"
split_action_caption = "AI 拆分"
changelog_action_caption = "AI changelog"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	if err := loadFile(&cfg, path); err != nil {
		t.Fatal(err)
	}
	if cfg.PrePush.BaseRef != "origin/develop" || !cfg.PrePush.Strict {
		t.Errorf("pre_push not parsed: %+v", cfg.PrePush)
	}
	if cfg.SourceTree.PRActionCaption != "AI PR" || cfg.SourceTree.PRShortcutKeyCode != 35 ||
		cfg.SourceTree.PRShortcutModifiers != 524288 || cfg.SourceTree.PRShortcutDisplay != "⌥P" ||
		cfg.SourceTree.ReviewBranchActionCaption != "AI 审分支" ||
		cfg.SourceTree.StashMsgActionCaption != "AI stash" ||
		cfg.SourceTree.PRTitleActionCaption != "AI PR 标题" ||
		cfg.SourceTree.ExplainActionCaption != "AI 解释" ||
		cfg.SourceTree.SplitActionCaption != "AI 拆分" ||
		cfg.SourceTree.ChangelogActionCaption != "AI changelog" {
		t.Errorf("sourcetree new keys not parsed: %+v", cfg.SourceTree)
	}
}

func TestValidateRejectsEmptyPrePushBaseRef(t *testing.T) {
	cfg := Default()
	cfg.PrePush.BaseRef = ""
	if err := cfg.validate(); err == nil {
		t.Errorf("empty pre_push.base_ref must be rejected")
	}
}

func TestValidateRejectsEmptyTypes(t *testing.T) {
	cfg := Default()
	cfg.Commit.Types = []string{}
	if err := cfg.validate(); err == nil {
		t.Errorf("empty commit.types must be rejected")
	}
}

func TestValidateRejectsUnsupportedStyle(t *testing.T) {
	cfg := Default()
	cfg.Commit.Style = "free"
	if err := cfg.validate(); err == nil {
		t.Errorf("unsupported commit.style must be rejected")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("STAI_BASE_URL", "http://example:9999/v1")
	t.Setenv("STAI_MODEL", "env-model")
	t.Setenv("STAI_LANG", "en")
	cfg := Default()
	applyEnv(&cfg)
	if cfg.Provider.BaseURL != "http://example:9999/v1" || cfg.Provider.Model != "env-model" {
		t.Errorf("env not applied: %+v", cfg.Provider)
	}
	if cfg.Global.Lang != "en" {
		t.Errorf("STAI_LANG not applied: %+v", cfg.Global)
	}
}

func TestNoCacheEnv(t *testing.T) {
	for _, tc := range []struct {
		value     string
		wantCache bool
	}{
		{"1", false},
		{"true", false},
		{"TRUE", false},
		{"0", true},
		{"false", true},
		{"False", true},
		{"no", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("STAI_NO_CACHE", tc.value)
			cfg := Default()
			applyEnv(&cfg)
			if cfg.Review.Cache != tc.wantCache {
				t.Errorf("STAI_NO_CACHE=%q: Cache=%v, want %v", tc.value, cfg.Review.Cache, tc.wantCache)
			}
		})
	}
}

func TestDeterministicForcesSeedAndCache(t *testing.T) {
	for _, tc := range []struct {
		deterministic bool
		seed          bool
		cache         bool
		wantSeed      bool
		wantCache     bool
	}{
		{true, false, false, true, true},
		{true, true, false, true, true},
		{true, false, true, true, true},
		{false, true, true, true, true},
		{false, false, false, false, false},
	} {
		t.Run(fmt.Sprintf("det=%v_seed=%v_cache=%v", tc.deterministic, tc.seed, tc.cache), func(t *testing.T) {
			// Isolate host env so only the struct fields drive applyEnv.
			t.Setenv("STAI_REVIEW_DETERMINISTIC", "")
			t.Setenv("STAI_REVIEW_SEED", "")
			t.Setenv("STAI_REVIEW_CACHE", "")
			t.Setenv("STAI_NO_CACHE", "")
			cfg := Default()
			cfg.Review.Deterministic = tc.deterministic
			cfg.Review.Seed = tc.seed
			cfg.Review.Cache = tc.cache
			applyEnv(&cfg)
			if cfg.Review.Seed != tc.wantSeed || cfg.Review.Cache != tc.wantCache {
				t.Errorf("got Seed=%v Cache=%v, want Seed=%v Cache=%v", cfg.Review.Seed, cfg.Review.Cache, tc.wantSeed, tc.wantCache)
			}
		})
	}
}

func TestSetBoolAcceptsCommonValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"0", false},
	} {
		var got bool
		if err := setBool(tc.raw, &got); err != nil {
			t.Errorf("setBool(%q) error: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("setBool(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if err := setBool("maybe", new(bool)); err == nil {
		t.Error("setBool(maybe) should fail")
	}
}

func TestLoadRuleSets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".stai.toml")
	content := `
[review]
rules = ["global rule"]

[[review.rule]]
include = ["*.go"]
exclude = ["*_test.go"]
rules = ["go rule", "shared"]

[[review.rule]]
include = ["*.js"]
rules = ["js rule"]
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := loadFile(&cfg, p); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Review.RuleSets) != 2 {
		t.Fatalf("got %d rule sets, want 2", len(cfg.Review.RuleSets))
	}
	rs := cfg.Review.RuleSets[0]
	if len(rs.Include) != 1 || rs.Include[0] != "*.go" {
		t.Errorf("include: %v", rs.Include)
	}
	if len(rs.Exclude) != 1 || rs.Exclude[0] != "*_test.go" {
		t.Errorf("exclude: %v", rs.Exclude)
	}
	if len(rs.Rules) != 2 {
		t.Errorf("rules: %v", rs.Rules)
	}

	// Matching
	rules, err := cfg.Review.RulesForPath("src/a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Errorf("src/a.go rules: %v", rules)
	}
	rules, err = cfg.Review.RulesForPath("src/a_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Errorf("a_test.go rules (excluded): %v", rules)
	}
	rules, err = cfg.Review.RulesForPath("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Errorf("app.js rules: %v", rules)
	}
	rules, err = cfg.Review.RulesForPath("docs/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Errorf("x.md rules: %v", rules)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "src/a.go", true}, // no slash: matches base name
		{"*.go", "src/a.txt", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "sub/src/a.go", false}, // has slash: full-path match only
		{"a?c.txt", "abc.txt", true},
		{"docs/*", "docs/x.md", true},
	}
	for _, c := range cases {
		got, err := globMatch(c.pattern, c.path)
		if err != nil {
			t.Errorf("globMatch(%q, %q) error: %v", c.pattern, c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestGlobMatchBadPatternErrors(t *testing.T) {
	if _, err := globMatch("[", "src/a.go"); err == nil {
		t.Errorf("globMatch(\"[\", ...) should report a bad-pattern error")
	}
}

func TestValidateRejectsBadRuleGlob(t *testing.T) {
	cfg := Default()
	cfg.Review.RuleSets = []ReviewRule{{Include: []string{"*["}, Rules: []string{"x"}}}
	if err := cfg.validate(); err == nil {
		t.Errorf("validate() should reject a malformed include glob")
	}
}
