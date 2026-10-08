package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileParsesSubset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
# global settings
[provider]
base_url = "http://192.168.1.10:1234/v1"
api_key  = 'sk-test'
model    = "deepseek-coder"

[commit]
style    = "free"
language = "en"

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
	if cfg.Provider.BaseURL != "http://192.168.1.10:1234/v1" ||
		cfg.Provider.APIKey != "sk-test" ||
		cfg.Provider.Model != "deepseek-coder" {
		t.Errorf("provider not parsed: %+v", cfg.Provider)
	}
	if cfg.Commit.Style != "free" || cfg.Commit.Language != "en" {
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

func TestEnvOverrides(t *testing.T) {
	t.Setenv("STAI_BASE_URL", "http://example:9999/v1")
	t.Setenv("STAI_MODEL", "env-model")
	cfg := Default()
	applyEnv(&cfg)
	if cfg.Provider.BaseURL != "http://example:9999/v1" || cfg.Provider.Model != "env-model" {
		t.Errorf("env not applied: %+v", cfg.Provider)
	}
}
