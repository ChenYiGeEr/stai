package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"stai/internal/i18n"
)

const (
	mergetoolCmdKey      = "mergetool.stai.cmd"
	mergetoolTrustKey    = "mergetool.stai.trustExitCode"
	mergetoolOwnsKey     = "mergetool.stai.staiOwnsMergeTool"
	mergetoolPreviousKey = "mergetool.stai.staiPreviousMergeTool"
	mergetoolScopeKey    = "mergetool.stai.staiMergeToolScope"
)

// installMergetool registers mergetool.stai for one repository.
// ask is non-nil only for an interactive install. It is called when
// merge.tool is already another tool; a true result is the only way that
// value is replaced. A nil ask leaves an occupied merge.tool alone.
// mergetool.stai.cmd and trustExitCode are written either way.
// auto_apply is not read.
func installMergetool(repo, exe string, ask func(prompt string) bool) error {
	cmd := shellQuote(exe) + ` mergetool --base "$BASE" --local "$LOCAL" --remote "$REMOTE" --merged "$MERGED"`
	if err := gitLocalSet(repo, mergetoolCmdKey, cmd); err != nil {
		return err
	}
	if err := gitLocalSet(repo, mergetoolTrustKey, "true"); err != nil {
		return err
	}
	fmt.Println(i18n.T("mergetool_registered"))

	cur, err := readMergeTool(repo)
	if err != nil {
		return err
	}
	owns, err := gitLocalGet(repo, mergetoolOwnsKey)
	if err != nil {
		return err
	}
	if cur.value == "" || cur.value == "stai" {
		if cur.value == "stai" && owns == "true" {
			fmt.Println(i18n.T("mergetool_tool_set"))
			return nil
		}
		return claimMergeTool(repo, cur)
	}
	if ask == nil || !ask(fmt.Sprintf(i18n.T("mergetool_tool_occupied"), cur.value)) {
		fmt.Printf(i18n.T("mergetool_tool_kept")+"\n", cur.value)
		return nil
	}
	return claimMergeTool(repo, cur)
}

// uninstallMergetool removes mergetool.stai.cmd and trustExitCode.
// When this install recorded that it set merge.tool to stai, the recorded
// previous value is restored. A merge.tool that names another tool is left
// as it is.
func uninstallMergetool(repo string) error {
	owns, err := gitLocalGet(repo, mergetoolOwnsKey)
	if err != nil {
		return err
	}
	scope, err := gitLocalGet(repo, mergetoolScopeKey)
	if err != nil {
		return err
	}
	prev, err := gitLocalGet(repo, mergetoolPreviousKey)
	if err != nil {
		return err
	}
	cur, err := readMergeTool(repo)
	if err != nil {
		return err
	}
	for _, key := range []string{mergetoolCmdKey, mergetoolTrustKey, mergetoolOwnsKey, mergetoolPreviousKey, mergetoolScopeKey} {
		if err := gitLocalUnset(repo, key); err != nil {
			return err
		}
	}
	if owns != "true" || cur.value != "stai" {
		if cur.value != "" && cur.value != "stai" {
			fmt.Printf(i18n.T("mergetool_tool_left")+"\n", cur.value)
		}
		fmt.Println(i18n.T("mergetool_removed"))
		return nil
	}
	switch scope {
	case "local":
		if prev == "" {
			if err := gitLocalUnset(repo, "merge.tool"); err != nil {
				return err
			}
			fmt.Println(i18n.T("mergetool_tool_cleared"))
		} else if err := gitLocalSet(repo, "merge.tool", prev); err != nil {
			return err
		} else {
			fmt.Printf(i18n.T("mergetool_tool_restored")+"\n", prev)
		}
	default:
		// "unset" and "foreign": the local key is the one stai wrote.
		// Removing it reveals the previous global/system value, if any.
		if err := gitLocalUnset(repo, "merge.tool"); err != nil {
			return err
		}
		after, err := readMergeTool(repo)
		if err != nil {
			return err
		}
		if after.value == "" {
			fmt.Println(i18n.T("mergetool_tool_cleared"))
		} else {
			fmt.Printf(i18n.T("mergetool_tool_restored")+"\n", after.value)
		}
	}
	fmt.Println(i18n.T("mergetool_removed"))
	return nil
}

type mergeTool struct {
	value string
	scope string // unset, local, foreign
}

func claimMergeTool(repo string, cur mergeTool) error {
	if err := gitLocalSet(repo, mergetoolOwnsKey, "true"); err != nil {
		return err
	}
	switch {
	case cur.value == "":
		if err := gitLocalUnset(repo, mergetoolPreviousKey); err != nil {
			return err
		}
		if err := gitLocalSet(repo, mergetoolScopeKey, "unset"); err != nil {
			return err
		}
	case cur.scope == "local":
		if err := gitLocalSet(repo, mergetoolPreviousKey, cur.value); err != nil {
			return err
		}
		if err := gitLocalSet(repo, mergetoolScopeKey, "local"); err != nil {
			return err
		}
	default:
		if err := gitLocalSet(repo, mergetoolPreviousKey, cur.value); err != nil {
			return err
		}
		if err := gitLocalSet(repo, mergetoolScopeKey, "foreign"); err != nil {
			return err
		}
	}
	if err := gitLocalSet(repo, "merge.tool", "stai"); err != nil {
		return err
	}
	fmt.Println(i18n.T("mergetool_tool_set"))
	return nil
}

func readMergeTool(repo string) (mergeTool, error) {
	localCfg, err := localGitConfigPath(repo)
	if err != nil {
		return mergeTool{}, err
	}
	out, missing, err := gitOutput(repo, "config", "--show-origin", "--get", "merge.tool")
	if err != nil {
		return mergeTool{}, err
	}
	if missing {
		return mergeTool{scope: "unset"}, nil
	}
	origin, value, ok := strings.Cut(out, "\t")
	if !ok {
		return mergeTool{}, fmt.Errorf("unexpected git config --show-origin output: %q", out)
	}
	origin = strings.TrimPrefix(origin, "file:")
	// git -C prints a repo-relative path when the config file is inside the
	// repository. Abs would resolve that against the process cwd.
	if !filepath.IsAbs(origin) {
		origin = filepath.Join(repo, origin)
	}
	if samePath(origin, localCfg) {
		return mergeTool{value: value, scope: "local"}, nil
	}
	return mergeTool{value: value, scope: "foreign"}, nil
}

func localGitConfigPath(repo string) (string, error) {
	out, missing, err := gitOutput(repo, "rev-parse", "--git-path", "config")
	if err != nil || missing {
		if err == nil {
			err = errors.New("git rev-parse returned no config path")
		}
		return "", err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(repo, out)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func gitLocalSet(repo, key, value string) error {
	_, err := gitRun(repo, "config", "--local", key, value)
	return err
}

func gitLocalGet(repo, key string) (string, error) {
	out, missing, err := gitOutput(repo, "config", "--local", "--get", key)
	if missing || err != nil {
		return "", err
	}
	return out, nil
}

func gitLocalUnset(repo, key string) error {
	cmd := exec.Command("git", "-C", repo, "config", "--local", "--unset", key)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	// 1 and 5: the key was already absent.
	if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 5) {
		return nil
	}
	return fmt.Errorf("git config --unset %s: %w: %s", key, err, strings.TrimSpace(string(out)))
}

// gitOutput runs git -C repo and returns stdout. A missing config key
// (--get exit 1) is reported as missing=true and a nil error.
func gitOutput(repo string, args ...string) (string, bool, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	text := strings.TrimSpace(string(out))
	if err == nil {
		return text, false, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "", true, nil
	}
	msg := text
	if ee != nil && len(ee.Stderr) > 0 {
		msg = strings.TrimSpace(string(ee.Stderr))
	}
	if msg != "" {
		return "", false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return "", false, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func gitRun(repo string, args ...string) (string, error) {
	out, missing, err := gitOutput(repo, args...)
	if missing {
		return "", fmt.Errorf("git %s: key not found", strings.Join(args, " "))
	}
	return out, err
}
