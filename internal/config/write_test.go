package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGlobalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := Default()
	cfg.Global.Lang = "en"
	cfg.Provider.BaseURL = "http://example.com/v1"
	cfg.Provider.APIKey = "secret"
	cfg.Provider.Model = "gpt-4"

	if err := WriteGlobal(cfg, "en"); err != nil {
		t.Fatalf("WriteGlobal failed: %v", err)
	}

	path := GlobalPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file permissions: got %o, want 0o600", info.Mode().Perm())
	}

	// Active wizard values must be present and uncommented.
	body := string(data)
	if !strings.Contains(body, `lang = "en"`) {
		t.Errorf("active lang missing")
	}
	if !strings.Contains(body, `base_url = "http://example.com/v1"`) {
		t.Errorf("active base_url missing")
	}
	if !strings.Contains(body, `api_key = "secret"`) {
		t.Errorf("active api_key missing")
	}
	if !strings.Contains(body, `model = "gpt-4"`) {
		t.Errorf("active model missing")
	}

	// Commented defaults must still be present.
	if !strings.Contains(body, `# timeout_seconds = 120`) {
		t.Errorf("commented default missing")
	}

	// It must be parseable and round-trip the active values.
	loaded := Default()
	if err := loadFile(&loaded, path); err != nil {
		t.Fatalf("parsing written config: %v", err)
	}
	if loaded.Global.Lang != "en" {
		t.Errorf("lang roundtrip: got %q", loaded.Global.Lang)
	}
	if loaded.Provider.BaseURL != cfg.Provider.BaseURL {
		t.Errorf("base_url roundtrip: got %q", loaded.Provider.BaseURL)
	}
	if loaded.Provider.APIKey != cfg.Provider.APIKey {
		t.Errorf("api_key roundtrip: got %q", loaded.Provider.APIKey)
	}
	if loaded.Provider.Model != cfg.Provider.Model {
		t.Errorf("model roundtrip: got %q", loaded.Provider.Model)
	}
}

func TestWriteGlobalZH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := Default()
	cfg.Global.Lang = "zh-CN"
	cfg.Provider.BaseURL = "http://localhost:11434/v1"
	cfg.Provider.APIKey = ""
	cfg.Provider.Model = "qwen2.5-coder:7b"

	if err := WriteGlobal(cfg, "zh-CN"); err != nil {
		t.Fatalf("WriteGlobal failed: %v", err)
	}

	path := filepath.Join(dir, ".config", "stai", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}
	if !strings.Contains(string(data), `lang = "zh-CN"`) {
		t.Errorf("zh-CN lang missing")
	}
}
