package mergetool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"stai/internal/i18n"
)

func TestMain(m *testing.M) {
	i18n.SetLang("zh-CN")
	os.Unsetenv("GIT_EDITOR")
	os.Unsetenv("VISUAL")
	os.Unsetenv("EDITOR")
	os.Exit(m.Run())
}

func TestFixture_mergetool_text_all_resolved(t *testing.T) {
	dir := copyFixture(t, "mergetool-text-all-resolved")
	code, out := runFixture(t, dir, "keys.txt", loadAdvice(t, filepath.Join(dir, "advice.json")), nil)
	got := mustRead(t, filepath.Join(dir, "merged.txt"))
	want := mustRead(t, filepath.Join(dir, "expected.txt"))
	wantExit := readExit(t, filepath.Join(dir, "exit.txt"))
	if code != wantExit {
		t.Fatalf("exit %d, want %d\nui:\n%s", code, wantExit, out)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
	}
	if bytes.Contains(got, []byte("<<<<<<<")) || bytes.Contains(got, []byte("=======")) || bytes.Contains(got, []byte(">>>>>>>")) {
		t.Fatalf("resolved file still has conflict markers:\n%s", got)
	}
	for _, forbidden := range []string{"SHOULD-NOT-APPLY-2", "SHOULD-NOT-APPLY-3"} {
		if bytes.Contains(got, []byte(forbidden)) {
			t.Fatalf("recommendation %s was written without the user pressing a for that hunk", forbidden)
		}
	}
	if !bytes.Contains(got, []byte("MERGED-1\n")) || !bytes.Contains(got, []byte("OURS-2\n")) || !bytes.Contains(got, []byte("THEIRS-3\n")) {
		t.Fatalf("resolved file is missing a chosen side:\n%s", got)
	}
}

func TestFixture_mergetool_text_skip(t *testing.T) {
	assertUnresolvedFixture(t, "mergetool-text-skip", "FORBIDDEN-REC-2", "ACCEPTED-1")
}

func TestFixture_mergetool_text_quit(t *testing.T) {
	assertUnresolvedFixture(t, "mergetool-text-quit", "FORBIDDEN-REC-Q2", "ACCEPTED-Q1")
}

func assertUnresolvedFixture(t *testing.T, id, forbidden, accepted string) {
	t.Helper()
	dir := copyFixture(t, id)
	code, out := runFixture(t, dir, "keys.txt", loadAdvice(t, filepath.Join(dir, "advice.json")), nil)
	got := mustRead(t, filepath.Join(dir, "merged.txt"))
	want := mustRead(t, filepath.Join(dir, "expected.txt"))
	wantExit := readExit(t, filepath.Join(dir, "exit.txt"))
	if code != wantExit || wantExit != 1 {
		t.Fatalf("exit %d, want 1\nui:\n%s", code, out)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
	}
	if !bytes.Contains(got, []byte("<<<<<<<")) {
		t.Fatal("unresolved hunk lost its conflict markers")
	}
	if bytes.Contains(got, []byte(forbidden)) {
		t.Fatalf("recommendation %s was written into a skipped or unprocessed hunk\n%s", forbidden, got)
	}
	if !bytes.Contains(got, []byte(accepted+"\n")) {
		t.Fatalf("the hunk the user accepted is missing %s\n%s", accepted, got)
	}
}

func TestFixture_mergetool_ai_failure(t *testing.T) {
	guess := mustRead(t, filepath.Join("testdata", "mergetool-ai-failure", "guess.txt"))
	if len(guess) == 0 {
		t.Fatal("guess fixture is empty")
	}

	t.Run("resolved", func(t *testing.T) {
		dir := copyFixture(t, "mergetool-ai-failure")
		code, out := runFixture(t, dir, "keys-resolved.txt", failAdvice(guess), nil)
		got := mustRead(t, filepath.Join(dir, "merged.txt"))
		want := mustRead(t, filepath.Join(dir, "expected-resolved.txt"))
		if code != readExit(t, filepath.Join(dir, "exit-resolved.txt")) {
			t.Fatalf("exit %d\nui:\n%s", code, out)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
		}
		assertNoGuess(t, got, out, guess)
		assertNoMarkers(t, got)
		assertFailureUI(t, out)
		if !strings.Contains(out, i18n.T("mergetool_a_unavailable")) {
			t.Fatalf("a was not rejected\n%s", out)
		}
		if !strings.Contains(out, i18n.T("mergetool_preview_no_rec")) {
			t.Fatalf("preview without a recommendation was not shown\n%s", out)
		}
	})

	t.Run("unresolved", func(t *testing.T) {
		dir := copyFixture(t, "mergetool-ai-failure")
		merged := filepath.Join(dir, "merged.txt")
		stamp := freeze(t, merged)
		code, out := runFixture(t, dir, "keys-unresolved.txt", failAdvice(guess), nil)
		got := mustRead(t, merged)
		want := mustRead(t, filepath.Join(dir, "expected-unresolved.txt"))
		if code != readExit(t, filepath.Join(dir, "exit-unresolved.txt")) {
			t.Fatalf("exit %d\nui:\n%s", code, out)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
		}
		assertFrozen(t, merged, stamp)
		if !bytes.Contains(got, []byte("<<<<<<<")) {
			t.Fatal("quit removed conflict markers")
		}
		assertNoGuess(t, got, out, guess)
		assertFailureUI(t, out)
	})

	t.Run("editor", func(t *testing.T) {
		dir := copyFixture(t, "mergetool-ai-failure")
		want := mustRead(t, filepath.Join(dir, "expected-editor.txt"))
		var calls atomic.Int32
		code, out := runFixture(t, dir, "keys-editor.txt", func(ctx context.Context, h Hunk) (Advice, error) {
			calls.Add(1)
			return Advice{Choice: "merge", Resolution: string(guess)}, errors.New("provider unreachable")
		}, func(path string) error {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Contains(body, []byte("<<<<<<<")) {
				t.Errorf("editor was opened on a file that no longer has conflict markers")
			}
			return os.WriteFile(path, want, 0o644)
		})
		got := mustRead(t, filepath.Join(dir, "merged.txt"))
		if code != readExit(t, filepath.Join(dir, "exit-editor.txt")) {
			t.Fatalf("exit %d\nui:\n%s", code, out)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
		}
		assertNoMarkers(t, got)
		assertNoGuess(t, got, out, guess)
		assertFailureUI(t, out)
		if calls.Load() != 1 {
			t.Fatalf("editor cleared every marker but AI was called %d times", calls.Load())
		}
	})

	t.Run("english", func(t *testing.T) {
		prev := i18n.Current()
		i18n.SetLang("en")
		t.Cleanup(func() { i18n.SetLang(prev) })
		dir := copyFixture(t, "mergetool-ai-failure")
		code, out := runFixture(t, dir, "keys-unresolved.txt", failAdvice(guess), nil)
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out, i18n.T("mergetool_ai_failed", "provider unreachable")) {
			t.Fatalf("english failure text missing\n%s", out)
		}
		if !strings.Contains(out, "keep ours") || !strings.Contains(out, "[q] quit") {
			t.Fatalf("english prompt missing\n%s", out)
		}
	})
}

func TestFixture_mergetool_unsupported_conflict(t *testing.T) {
	root := copyFixture(t, "mergetool-unsupported-conflict")
	wantExit := readExit(t, filepath.Join(root, "exit.txt"))
	if wantExit != 1 {
		t.Fatalf("fixture exit.txt = %d, want 1", wantExit)
	}

	t.Run("binary", func(t *testing.T) {
		dir := filepath.Join(root, "binary")
		merged := filepath.Join(dir, "merged")
		before := mustRead(t, merged)
		stamp := freeze(t, merged)
		code, out := runRefused(t, Options{
			Base: filepath.Join(dir, "base"), Local: filepath.Join(dir, "local"),
			Remote: filepath.Join(dir, "remote"), Merged: merged,
		})
		assertRefused(t, "binary", code, out, merged, before, stamp)
	})

	t.Run("delete", func(t *testing.T) {
		dir := filepath.Join(root, "delete")
		merged := filepath.Join(dir, "merged")
		before := mustRead(t, merged)
		stamp := freeze(t, merged)
		code, out := runRefused(t, Options{
			Base: filepath.Join(dir, "base"), Local: filepath.Join(dir, "local"),
			Remote: filepath.Join(dir, "remote"), Merged: merged,
		})
		if _, err := os.Stat(filepath.Join(dir, "local")); !os.IsNotExist(err) {
			t.Fatalf("delete fixture local side exists: %v", err)
		}
		assertRefused(t, "delete", code, out, merged, before, stamp)
	})

	t.Run("submodule", func(t *testing.T) {
		dir := filepath.Join(root, "submodule")
		merged := filepath.Join(dir, "merged")
		before := mustRead(t, merged)
		stamp := freeze(t, merged)
		code, out := runRefused(t, Options{
			Base: filepath.Join(dir, "base"), Local: filepath.Join(dir, "local"),
			Remote: filepath.Join(dir, "remote"), Merged: merged,
		})
		assertRefused(t, "submodule", code, out, merged, before, stamp)
	})

	t.Run("rename", func(t *testing.T) {
		repo := t.TempDir()
		body := mustRead(t, filepath.Join(root, "rename", "body.txt"))
		gitRepo(t, repo, body)
		for _, name := range []string{"old.txt", "ours.txt", "theirs.txt"} {
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(repo, name)
				var before []byte
				var stamp time.Time
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					before = mustRead(t, path)
					stamp = freeze(t, path)
				} else if !os.IsNotExist(err) && err != nil {
					t.Fatal(err)
				}
				code, out := runRefused(t, Options{
					Base: filepath.Join(repo, "ours.txt"), Local: filepath.Join(repo, "ours.txt"),
					Remote: filepath.Join(repo, "theirs.txt"), Merged: path,
				})
				if code != 1 {
					t.Fatalf("exit %d\nui:\n%s", code, out)
				}
				wantMsg := i18n.T("mergetool_unsupported", i18n.T("mergetool_kind_rename"))
				if !strings.Contains(out, wantMsg) {
					t.Fatalf("ui %q, want %q", out, wantMsg)
				}
				if strings.Contains(out, "[o] 保留 ours") {
					t.Fatalf("rename conflict entered the text prompt\n%s", out)
				}
				if before == nil {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("missing %s was created: %v", name, err)
					}
					return
				}
				got := mustRead(t, path)
				if !bytes.Equal(got, before) {
					t.Fatalf("%s changed\n got %q\nwant %q", name, got, before)
				}
				assertFrozen(t, path, stamp)
			})
		}
	})

	t.Run("gitlink", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		git(t, repo, "commit", "--allow-empty", "-m", "a")
		sha1 := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", "f.txt")
		git(t, repo, "commit", "-m", "b")
		sha2 := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("c\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", "f.txt")
		git(t, repo, "commit", "-m", "c")
		sha3 := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
		var records strings.Builder
		for _, rec := range []struct{ sha, stage string }{{sha1, "1"}, {sha2, "2"}, {sha3, "3"}} {
			records.WriteString("160000 " + rec.sha + " " + rec.stage + "\tlib\n")
		}
		cmd := exec.Command("git", "-C", repo, "update-index", "--index-info")
		cmd.Stdin = strings.NewReader(records.String())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index: %v\n%s", err, out)
		}
		lib := filepath.Join(repo, "lib")
		code, out := runRefused(t, Options{Base: lib, Local: lib, Remote: lib, Merged: lib})
		if code != 1 {
			t.Fatalf("exit %d\nui:\n%s", code, out)
		}
		wantMsg := i18n.T("mergetool_unsupported", i18n.T("mergetool_kind_submodule"))
		if !strings.Contains(out, wantMsg) {
			t.Fatalf("ui %q, want %q", out, wantMsg)
		}
		if _, err := os.Stat(lib); !os.IsNotExist(err) {
			t.Fatalf("gitlink path was created: %v", err)
		}
	})
}

func assertRefused(t *testing.T, kind string, code int, out, merged string, before []byte, stamp time.Time) {
	t.Helper()
	if code != 1 {
		t.Fatalf("%s exit %d\nui:\n%s", kind, code, out)
	}
	got := mustRead(t, merged)
	if !bytes.Equal(got, before) {
		t.Fatalf("%s $MERGED changed\n got %q\nwant %q", kind, got, before)
	}
	assertFrozen(t, merged, stamp)
	key := "mergetool_kind_" + kind
	wantMsg := i18n.T("mergetool_unsupported", i18n.T(key))
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("%s ui %q, want %q", kind, out, wantMsg)
	}
	if strings.Contains(out, "[o] 保留 ours") || strings.Contains(out, i18n.T("mergetool_done")) {
		t.Fatalf("%s entered the text resolver\n%s", kind, out)
	}
}

func assertFailureUI(t *testing.T, out string) {
	t.Helper()
	if !strings.Contains(out, i18n.T("mergetool_ai_failed", "provider unreachable")) {
		t.Fatalf("failure was not reported\n%s", out)
	}
	prompt := i18n.T("mergetool_prompt_no_a")
	if !strings.Contains(out, prompt) {
		t.Fatalf("prompt missing %q\n%s", prompt, out)
	}
	for _, key := range []string{"[o]", "[t]", "[e]", "[s]", "[q]"} {
		if !strings.Contains(prompt, key) {
			t.Fatalf("prompt %q does not offer %s", prompt, key)
		}
	}
}

func assertNoGuess(t *testing.T, file []byte, out string, guess []byte) {
	t.Helper()
	if bytes.Contains(file, guess) {
		t.Fatalf("model guess written to $MERGED: %q", file)
	}
	if strings.Contains(out, string(guess)) {
		t.Fatalf("model guess shown as a recommendation\n%s", out)
	}
}

func assertNoMarkers(t *testing.T, file []byte) {
	t.Helper()
	if bytes.Contains(file, []byte("<<<<<<<")) || bytes.Contains(file, []byte(">>>>>>>")) {
		t.Fatalf("markers remain:\n%s", file)
	}
}

type adviceFile struct {
	Hunks []Advice `json:"hunks"`
}

func loadAdvice(t *testing.T, path string) func(context.Context, Hunk) (Advice, error) {
	t.Helper()
	var file adviceFile
	if err := json.Unmarshal(mustRead(t, path), &file); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	return func(context.Context, Hunk) (Advice, error) {
		i := int(n.Add(1)) - 1
		if i >= len(file.Hunks) {
			t.Errorf("advice called %d times, fixture has %d hunks", i+1, len(file.Hunks))
			return Advice{}, errors.New("no advice")
		}
		return file.Hunks[i], nil
	}
}

func failAdvice(guess []byte) func(context.Context, Hunk) (Advice, error) {
	return func(context.Context, Hunk) (Advice, error) {
		return Advice{Explanation: "ignore", Choice: "merge", Resolution: string(guess)}, errors.New("provider unreachable")
	}
}

func runFixture(t *testing.T, dir, keysName string, advise func(context.Context, Hunk) (Advice, error), edit func(string) error) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base:   filepath.Join(dir, "base.txt"),
		Local:  filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"),
		Merged: filepath.Join(dir, "merged.txt"),
		In:     bytes.NewReader(mustRead(t, filepath.Join(dir, keysName))),
		Out:    &buf,
		Advise: advise,
		Edit:   edit,
	})
	return code, buf.String()
}

type bombReader struct{ hit atomic.Bool }

func (b *bombReader) Read([]byte) (int, error) {
	b.hit.Store(true)
	return 0, errors.New("key read")
}

func runRefused(t *testing.T, opt Options) (int, string) {
	t.Helper()
	keys := &bombReader{}
	var advised atomic.Bool
	var buf bytes.Buffer
	opt.In = keys
	opt.Out = &buf
	opt.Advise = func(context.Context, Hunk) (Advice, error) {
		advised.Store(true)
		return Advice{Choice: "merge", Resolution: "GUESSED-RESOLUTION\n"}, nil
	}
	code := Run(context.Background(), opt)
	out := buf.String()
	if keys.hit.Load() {
		t.Fatalf("refused conflict waited for a key\n%s", out)
	}
	if advised.Load() {
		t.Fatalf("refused conflict called the model\n%s", out)
	}
	if bytes.Contains(mustReadMaybe(t, opt.Merged), []byte("GUESSED-RESOLUTION")) {
		t.Fatal("refused conflict wrote a guessed resolution")
	}
	return code, out
}

func copyFixture(t *testing.T, id string) string {
	t.Helper()
	src := filepath.Join("testdata", id)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustReadMaybe(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readExit(t *testing.T, path string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(string(mustRead(t, path))))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func freeze(t *testing.T, path string) time.Time {
	t.Helper()
	ts := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

func assertFrozen(t *testing.T, path string, ts time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(ts) {
		t.Fatalf("%s mtime changed to %s (file was rewritten)", path, info.ModTime().Format(time.RFC3339Nano))
	}
}

func gitInit(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.email", "stai@example.com")
	git(t, repo, "config", "user.name", "stai")
	git(t, repo, "config", "core.autocrlf", "false")
	git(t, repo, "config", "core.quotepath", "false")
}

func gitRepo(t *testing.T, repo string, body []byte) {
	t.Helper()
	gitInit(t, repo)
	old := filepath.Join(repo, "old.txt")
	if err := os.WriteFile(old, body, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "old.txt")
	git(t, repo, "commit", "-q", "-m", "base")
	git(t, repo, "branch", "other")
	git(t, repo, "mv", "old.txt", "ours.txt")
	git(t, repo, "commit", "-q", "-m", "ours-rename")
	git(t, repo, "checkout", "-q", "other")
	git(t, repo, "mv", "old.txt", "theirs.txt")
	git(t, repo, "commit", "-q", "-m", "theirs-rename")
	cmd := exec.Command("git", "-C", repo, "merge", "--no-commit", "main")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a rename conflict, merge succeeded\n%s", out)
	}
	if !bytes.Contains(out, []byte("CONFLICT (rename")) && !bytes.Contains(mustReadMaybe(t, filepath.Join(repo, ".git", "MERGE_HEAD")), []byte{}) {
		status := git(t, repo, "status")
		if !strings.Contains(status, "CONFLICT (rename") {
			t.Fatalf("merge did not produce a rename conflict\n%s\nstatus:\n%s", out, status)
		}
	}
}

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
