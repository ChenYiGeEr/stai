package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"stai/internal/i18n"
)

const fixtureExe = "/opt/stai/bin/stai"

func TestFixture_install_mergetool_free(t *testing.T) {
	repo := newIsolatedRepo(t)
	if err := installMergetool(repo, fixtureExe, nil); err != nil {
		t.Fatal(err)
	}
	assertConfigFixture(t, repo, "install-mergetool-free/expected.txt")
	assertCmd(t, repo, "install-mergetool-free/cmd.suffix")
}

func TestFixture_install_mergetool_occupied(t *testing.T) {
	t.Run("agree", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		applyFixtureConfig(t, repo, "install-mergetool-occupied/before.txt")
		var prompts []string
		err := installMergetool(repo, fixtureExe, func(prompt string) bool {
			prompts = append(prompts, prompt)
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(prompts) != 1 || !strings.Contains(prompts[0], "vimdiff") || !strings.Contains(prompts[0], i18n.T("mergetool_tool_occupied", "vimdiff")) {
			t.Fatalf("occupied prompt = %q", prompts)
		}
		assertConfigFixture(t, repo, "install-mergetool-occupied/agree-expected.txt")
		assertCmd(t, repo, "install-mergetool-free/cmd.suffix")
	})

	t.Run("refuse", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		applyFixtureConfig(t, repo, "install-mergetool-occupied/before.txt")
		asked := false
		err := installMergetool(repo, fixtureExe, func(prompt string) bool {
			asked = true
			if !strings.Contains(prompt, "vimdiff") {
				t.Fatalf("prompt = %q", prompt)
			}
			return false
		})
		if err != nil {
			t.Fatal(err)
		}
		if !asked {
			t.Fatal("occupied install did not ask")
		}
		assertConfigFixture(t, repo, "install-mergetool-occupied/refuse-expected.txt")
		assertCmd(t, repo, "install-mergetool-free/cmd.suffix")
		if got := gitGet(t, repo, "merge.tool"); got != "vimdiff" {
			t.Fatalf("merge.tool = %q, want vimdiff", got)
		}
	})

	t.Run("noninteractive", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		applyFixtureConfig(t, repo, "install-mergetool-occupied/before.txt")
		if err := installMergetool(repo, fixtureExe, nil); err != nil {
			t.Fatal(err)
		}
		assertConfigFixture(t, repo, "install-mergetool-occupied/noninteractive-expected.txt")
		assertCmd(t, repo, "install-mergetool-free/cmd.suffix")
		if got := gitGet(t, repo, "merge.tool"); got != "vimdiff" {
			t.Fatalf("merge.tool = %q, want vimdiff", got)
		}
	})

	t.Run("english-prompt", func(t *testing.T) {
		prev := i18n.Current()
		i18n.SetLang("en")
		t.Cleanup(func() { i18n.SetLang(prev) })
		repo := newIsolatedRepo(t)
		applyFixtureConfig(t, repo, "install-mergetool-occupied/before.txt")
		var prompt string
		if err := installMergetool(repo, fixtureExe, func(p string) bool {
			prompt = p
			return false
		}); err != nil {
			t.Fatal(err)
		}
		if prompt != i18n.T("mergetool_tool_occupied", "vimdiff") {
			t.Fatalf("english prompt = %q", prompt)
		}
	})
}

func TestFixture_uninstall_mergetool_restore(t *testing.T) {
	t.Run("from-unset", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		if err := installMergetool(repo, fixtureExe, nil); err != nil {
			t.Fatal(err)
		}
		if gitGet(t, repo, "merge.tool") != "stai" {
			t.Fatal("install did not set merge.tool")
		}
		if err := uninstallMergetool(repo); err != nil {
			t.Fatal(err)
		}
		assertConfigFixture(t, repo, "uninstall-mergetool-restore/from-unset-expected.txt")
	})

	t.Run("from-agree", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		applyFixtureConfig(t, repo, "install-mergetool-occupied/before.txt")
		if err := installMergetool(repo, fixtureExe, func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
		// A later install, while merge.tool is still stai, must keep the
		// recorded previous tool instead of treating stai as the original.
		if err := installMergetool(repo, fixtureExe, func(string) bool {
			t.Fatal("reinstall must not ask when merge.tool is already stai")
			return false
		}); err != nil {
			t.Fatal(err)
		}
		if gitGet(t, repo, mergetoolPreviousKey) != "vimdiff" {
			t.Fatalf("previous tool lost: %q", gitGet(t, repo, mergetoolPreviousKey))
		}
		if err := uninstallMergetool(repo); err != nil {
			t.Fatal(err)
		}
		assertConfigFixture(t, repo, "uninstall-mergetool-restore/from-agree-expected.txt")
	})

	t.Run("other-tool", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		if err := installMergetool(repo, fixtureExe, nil); err != nil {
			t.Fatal(err)
		}
		gitSet(t, repo, "merge.tool", "opendiff")
		if err := uninstallMergetool(repo); err != nil {
			t.Fatal(err)
		}
		assertConfigFixture(t, repo, "uninstall-mergetool-restore/other-tool-expected.txt")
	})

	t.Run("foreign-global", func(t *testing.T) {
		repo := newIsolatedRepo(t)
		global := os.Getenv("GIT_CONFIG_GLOBAL")
		gitGlobal(t, global, "merge.tool", "vimdiff")
		if err := installMergetool(repo, fixtureExe, func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if gitGet(t, repo, "merge.tool") != "stai" {
			t.Fatalf("effective merge.tool = %q", gitGet(t, repo, "merge.tool"))
		}
		if gitGet(t, repo, mergetoolScopeKey) != "foreign" || gitGet(t, repo, mergetoolPreviousKey) != "vimdiff" {
			t.Fatalf("scope=%q previous=%q", gitGet(t, repo, mergetoolScopeKey), gitGet(t, repo, mergetoolPreviousKey))
		}
		if err := uninstallMergetool(repo); err != nil {
			t.Fatal(err)
		}
		if _, ok := gitGetLocal(t, repo, "merge.tool"); ok {
			t.Fatal("local merge.tool should be unset so the global value shows through")
		}
		if got := gitGet(t, repo, "merge.tool"); got != "vimdiff" {
			t.Fatalf("effective merge.tool = %q, want vimdiff", got)
		}
		if _, ok := gitGetLocal(t, repo, mergetoolCmdKey); ok {
			t.Fatal("mergetool.stai.cmd still set")
		}
		if _, ok := gitGetLocal(t, repo, mergetoolTrustKey); ok {
			t.Fatal("trustExitCode still set")
		}
		// The global file itself still names vimdiff and does not gain mergetool.stai.
		data, err := os.ReadFile(global)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "vimdiff") || strings.Contains(text, "mergetool") {
			t.Fatalf("global gitconfig changed:\n%s", text)
		}
	})
}

func newIsolatedRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	repo := t.TempDir()
	gitCmd(t, repo, "init", "-q", "-b", "main")
	return repo
}

func applyFixtureConfig(t *testing.T, repo, rel string) {
	t.Helper()
	for _, line := range fixtureLines(t, rel) {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(key, "!") {
			t.Fatalf("fixture %s has a non-assignment %q", rel, line)
		}
		gitSet(t, repo, key, value)
	}
}

func assertConfigFixture(t *testing.T, repo, rel string) {
	t.Helper()
	for _, line := range fixtureLines(t, rel) {
		if key, ok := strings.CutPrefix(line, "!"); ok {
			if value, found := gitGetLocal(t, repo, key); found {
				t.Fatalf("key %s = %q, want it absent", key, value)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("bad fixture line %q", line)
		}
		got, found := gitGetLocal(t, repo, key)
		if !found {
			t.Fatalf("key %s missing, want %q", key, value)
		}
		if got != value {
			t.Fatalf("key %s = %q, want %q", key, got, value)
		}
	}
}

func assertCmd(t *testing.T, repo, suffixRel string) {
	t.Helper()
	suffix := strings.TrimSpace(string(mustReadTest(t, suffixRel)))
	got, found := gitGetLocal(t, repo, mergetoolCmdKey)
	if !found {
		t.Fatal("mergetool.stai.cmd missing")
	}
	want := shellQuote(fixtureExe) + " " + suffix
	if got != want {
		t.Fatalf("cmd = %q\nwant %q", got, want)
	}
	for _, token := range []string{`$BASE`, `$LOCAL`, `$REMOTE`, `$MERGED`, "--base", "--local", "--remote", "--merged"} {
		if !strings.Contains(got, token) {
			t.Fatalf("cmd %q does not contain %s", got, token)
		}
	}
}

func fixtureLines(t *testing.T, rel string) []string {
	t.Helper()
	text := string(mustReadTest(t, rel))
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func mustReadTest(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gitSet(t *testing.T, repo, key, value string) {
	t.Helper()
	gitCmd(t, repo, "config", "--local", key, value)
}

func gitGlobal(t *testing.T, file, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--file", file, key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config --file: %v\n%s", err, out)
	}
}

func gitGet(t *testing.T, repo, key string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

func gitGetLocal(t *testing.T, repo, key string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "config", "--local", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", false
		}
		t.Fatalf("git config --local --get %s: %v", key, err)
	}
	return strings.TrimSpace(string(out)), true
}

func gitCmd(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
