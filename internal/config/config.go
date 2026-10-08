// Package config loads stai's layered configuration: built-in defaults,
// environment overrides, a global file (~/.config/stai/config.toml) and an
// optional per-repo .stai.toml whose fields override everything else.
package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Provider struct {
	BaseURL string
	APIKey  string
	Model   string
}

type Commit struct {
	Style    string // "conventional" or "free"
	Language string // e.g. "zh-CN"
}

type Review struct {
	Strict bool // true: pre-commit blocks on high-severity findings
}

type Config struct {
	Provider Provider
	Commit   Commit
	Review   Review
}

func Default() Config {
	return Config{
		Provider: Provider{
			BaseURL: "http://localhost:11434/v1", // local Ollama by default
			APIKey:  "",
			Model:   "qwen2.5-coder:7b",
		},
		Commit: Commit{
			Style:    "conventional",
			Language: "zh-CN",
		},
		Review: Review{Strict: false},
	}
}

// Load builds the effective configuration for the current repository.
func Load() (Config, error) {
	cfg := Default()
	applyEnv(&cfg)

	home, err := os.UserHomeDir()
	if err == nil {
		if err := loadFile(&cfg, filepath.Join(home, ".config", "stai", "config.toml")); err != nil && !os.IsNotExist(err) {
			return cfg, fmt.Errorf("global config: %w", err)
		}
	}

	if root, err := repoRoot(); err == nil {
		if err := loadFile(&cfg, filepath.Join(root, ".stai.toml")); err != nil && !os.IsNotExist(err) {
			return cfg, fmt.Errorf("repo config: %w", err)
		}
	}
	return cfg, nil
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

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// loadFile parses a minimal TOML subset: [section] headers, scalar
// key = value pairs (strings, booleans), # comments. That covers every
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
		if line == "" || strings.HasPrefix(line, "#") {
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

func set(cfg *Config, section, key, raw string) error {
	val, err := parseScalar(raw)
	if err != nil {
		return err
	}
	switch section + "." + key {
	case "provider.base_url":
		cfg.Provider.BaseURL = val
	case "provider.api_key":
		cfg.Provider.APIKey = val
	case "provider.model":
		cfg.Provider.Model = val
	case "commit.style":
		cfg.Commit.Style = val
	case "commit.language":
		cfg.Commit.Language = val
	case "review.strict":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("review.strict: %w", err)
		}
		cfg.Review.Strict = b
	default:
		return fmt.Errorf("unknown key %q in section [%s]", key, section)
	}
	return nil
}

// parseScalar handles "double-quoted strings", 'single-quoted' and bare
// booleans.
func parseScalar(raw string) (string, error) {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			return raw[1 : len(raw)-1], nil
		}
	}
	switch raw {
	case "true", "false":
		return raw, nil
	}
	return "", fmt.Errorf("unsupported value %q (use a quoted string or boolean)", raw)
}
