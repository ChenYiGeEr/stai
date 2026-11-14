package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/i18n"
)

type recordedNote struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Body     string `json:"body"`
}

type exitPanic int

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "fixtures", name+".json")
}

func readFixture(t *testing.T, name string, dst any) {
	t.Helper()
	b, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Errorf("chdir %s: %v", wd, err)
		}
	})
}

// stderrCapture records os.Stderr. os.Stderr is an *os.File, so the capture
// is a pipe drained on String.
type stderrCapture struct {
	buf  bytes.Buffer
	prev *os.File
	w    *os.File
	once sync.Once
	done chan struct{}
}

func captureStderr(t *testing.T) *stderrCapture {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cap := &stderrCapture{prev: os.Stderr, w: w, done: make(chan struct{})}
	os.Stderr = w
	go func() {
		_, _ = io.Copy(&cap.buf, r)
		close(cap.done)
	}()
	t.Cleanup(func() {
		cap.String()
		_ = r.Close()
	})
	return cap
}

func (c *stderrCapture) String() string {
	c.once.Do(func() {
		if c.prev != nil {
			os.Stderr = c.prev
			c.prev = nil
		}
		_ = c.w.Close()
		<-c.done
	})
	return c.buf.String()
}

func captureNotes(t *testing.T) *[]recordedNote {
	t.Helper()
	notes := []recordedNote{}
	prev := notifySink
	notifySink = func(title, subtitle, body string) {
		notes = append(notes, recordedNote{Title: title, Subtitle: subtitle, Body: body})
	}
	t.Cleanup(func() { notifySink = prev })
	return &notes
}

func runExit(fn func()) (code int) {
	prev := exitFunc
	exitFunc = func(c int) { panic(exitPanic(c)) }
	defer func() { exitFunc = prev }()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if c, ok := r.(exitPanic); ok {
			code = int(c)
			return
		}
		panic(r)
	}()
	fn()
	return 0
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=stai",
		"GIT_AUTHOR_EMAIL=test@example.test",
		"GIT_COMMITTER_NAME=stai",
		"GIT_COMMITTER_EMAIL=test@example.test",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.test")
	gitRun(t, dir, "config", "user.name", "stai")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func writeRepoFile(t *testing.T, repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STAI_LANG", "")
	t.Setenv("STAI_DISABLE", "")
	t.Setenv("STAI_API_KEY", "")
	t.Setenv("STAI_MODEL", "fixture-model")
	t.Cleanup(func() {
		i18n.SetLang("zh-CN")
		ai.SetLang("zh-CN")
	})
	return home
}

func modelServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"feat: 增加说明"}}]}`)
		case "error":
			http.Error(w, "model exploded", http.StatusInternalServerError)
		default:
			t.Errorf("model endpoint called (mode %q) %s", mode, r.URL.Path)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertNotes(t *testing.T, got []recordedNote, want *recordedNote) {
	t.Helper()
	if want == nil {
		if len(got) != 0 {
			t.Fatalf("notifications = %+v, want none", got)
		}
		return
	}
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one %+v", got, *want)
	}
	if got[0] != *want {
		t.Fatalf("notification = %+v, want %+v", got[0], *want)
	}
}

type actionFixture struct {
	ID           string        `json:"id"`
	Command      string        `json:"command"`
	ExitCode     int           `json:"exit_code"`
	Stderr       string        `json:"stderr"`
	Notification *recordedNote `json:"notification"`
	Stage        bool          `json:"stage"`
	Model        string        `json:"model"`
}

func TestActionFailNotifyFixtures(t *testing.T) {
	for _, id := range []string{
		"action-fail-notify-empty-index",
		"action-fail-notify-model-error",
	} {
		t.Run(id, func(t *testing.T) {
			var fx actionFixture
			readFixture(t, id, &fx)
			if fx.ID != id {
				t.Fatalf("fixture id = %q", fx.ID)
			}
			if fx.Command != "stai gen" {
				t.Fatalf("fixture command = %q, test invokes stai gen", fx.Command)
			}

			repo := initRepo(t)
			writeRepoFile(t, repo, "a.txt", "hello\n")
			if fx.Stage {
				gitRun(t, repo, "add", "a.txt")
			}
			isolateHome(t)
			srv := modelServer(t, fx.Model)
			t.Setenv("STAI_BASE_URL", srv.URL)
			chdir(t, repo)

			stderr := captureStderr(t)
			notes := captureNotes(t)
			code := runExit(func() { cmdGen(nil) })

			if code != fx.ExitCode {
				t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, fx.ExitCode, stderr.String())
			}
			if stderr.String() != fx.Stderr {
				t.Fatalf("stderr = %q, want %q", stderr.String(), fx.Stderr)
			}
			assertNotes(t, *notes, fx.Notification)
		})
	}
}

func TestStashMsgEmptyWorktreeNotifies(t *testing.T) {
	repo := initRepo(t)
	writeRepoFile(t, repo, "a.txt", "hello\n")
	gitRun(t, repo, "add", "a.txt")
	gitRun(t, repo, "commit", "-m", "init")
	isolateHome(t)
	srv := modelServer(t, "none")
	t.Setenv("STAI_BASE_URL", srv.URL)
	chdir(t, repo)

	stderr := captureStderr(t)
	notes := captureNotes(t)
	code := runExit(func() { cmdStashMsg(nil) })

	want := "stai: " + i18n.T("worktree_empty")
	if code != 1 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if stderr.String() != want+"\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want+"\n")
	}
	assertNotes(t, *notes, &recordedNote{Title: "stai", Subtitle: "", Body: want})
}

func TestPRTitleNoCommitsAheadNotifiesAndExits0(t *testing.T) {
	repo := initRepo(t)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")
	home := isolateHome(t)
	cfgDir := filepath.Join(home, ".config", "stai")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[pre_push]\nbase_ref = \"main\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := modelServer(t, "none")
	t.Setenv("STAI_BASE_URL", srv.URL)
	chdir(t, repo)

	stderr := captureStderr(t)
	notes := captureNotes(t)
	code := runExit(func() { cmdPRTitle(nil) })

	want := "当前分支没有领先 main 的提交"
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (empty ahead is not a blocked commit)\nstderr:\n%s", code, stderr.String())
	}
	if stderr.String() != want+"\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want+"\n")
	}
	assertNotes(t, *notes, &recordedNote{Title: "stai", Subtitle: "", Body: want})
}

func TestPRTitleModelErrorNotifies(t *testing.T) {
	repo := initRepo(t)
	gitRun(t, repo, "commit", "--allow-empty", "-m", "init")
	gitRun(t, repo, "checkout", "-b", "feature")
	writeRepoFile(t, repo, "a.txt", "hello\n")
	gitRun(t, repo, "add", "a.txt")
	gitRun(t, repo, "commit", "-m", "feat: add file")

	home := isolateHome(t)
	cfgDir := filepath.Join(home, ".config", "stai")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[pre_push]\nbase_ref = \"main\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := modelServer(t, "error")
	t.Setenv("STAI_BASE_URL", srv.URL)
	chdir(t, repo)

	stderr := captureStderr(t)
	notes := captureNotes(t)
	code := runExit(func() { cmdPRTitle(nil) })

	wantBody := "stai: provider returned 500 Internal Server Error: model exploded"
	if code != 1 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if stderr.String() != wantBody+"\n" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantBody+"\n")
	}
	assertNotes(t, *notes, &recordedNote{Title: "stai", Subtitle: "", Body: wantBody})
}

type hookFixture struct {
	ID                 string        `json:"id"`
	Command            string        `json:"command"`
	ExitCode           int           `json:"exit_code"`
	Stderr             string        `json:"stderr"`
	Notification       *recordedNote `json:"notification"`
	NotificationEN     *recordedNote `json:"notification_en"`
	InitialMessageFile string        `json:"initial_message_file"`
	MessageFile        string        `json:"message_file"`
	Source             string        `json:"source"`
	Disable            bool          `json:"disable"`
	Stage              bool          `json:"stage"`
	Model              string        `json:"model"`
}

func TestHookNotifyFixtures(t *testing.T) {
	ids := []string{
		"hook-notify-written",
		"hook-notify-skip-existing",
		"hook-notify-skip-merge",
		"hook-notify-skip-squash",
		"hook-notify-skip-amend",
		"hook-notify-skip-empty-index",
		"hook-notify-skip-disabled",
		"hook-notify-fail-exit0",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			var fx hookFixture
			readFixture(t, id, &fx)
			if fx.ID != id {
				t.Fatalf("fixture id = %q", fx.ID)
			}
			runHookFixture(t, fx, "")
			if fx.NotificationEN != nil {
				t.Run("STAI_LANG=en", func(t *testing.T) {
					runHookFixture(t, fx, "env")
				})
				t.Run("config-lang-en", func(t *testing.T) {
					runHookFixture(t, fx, "file")
				})
			}
		})
	}
}

// langMode is "", "env" (STAI_LANG=en), or "file" ([global] lang = "en").
func runHookFixture(t *testing.T, fx hookFixture, langMode string) {
	t.Helper()
	wantCmd := "stai hook prepare-commit-msg <msg-file>"
	if fx.Source != "" {
		wantCmd += " " + fx.Source
	}
	if fx.Disable {
		wantCmd = "STAI_DISABLE=1 " + wantCmd
	}
	if fx.Command != wantCmd {
		t.Fatalf("fixture command = %q, test invokes %q", fx.Command, wantCmd)
	}

	repo := initRepo(t)
	writeRepoFile(t, repo, "a.txt", "hello\n")
	if fx.Stage {
		gitRun(t, repo, "add", "a.txt")
	}
	msgFile := filepath.Join(repo, "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(fx.InitialMessageFile), 0o644); err != nil {
		t.Fatal(err)
	}

	home := isolateHome(t)
	if langMode == "file" {
		cfgDir := filepath.Join(home, ".config", "stai")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "[global]\nlang = \"en\"\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if langMode == "env" {
		t.Setenv("STAI_LANG", "en")
	}
	if fx.Disable {
		t.Setenv("STAI_DISABLE", "1")
	}
	srv := modelServer(t, fx.Model)
	t.Setenv("STAI_BASE_URL", srv.URL)
	chdir(t, repo)

	stderr := captureStderr(t)
	notes := captureNotes(t)
	args := []string{"prepare-commit-msg", msgFile}
	if fx.Source != "" {
		args = append(args, fx.Source)
	}
	code := runExit(func() { cmdHook(args) })

	if code != fx.ExitCode {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, fx.ExitCode, stderr.String())
	}
	if stderr.String() != fx.Stderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), fx.Stderr)
	}
	got, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fx.MessageFile {
		t.Fatalf("message file = %q, want %q", got, fx.MessageFile)
	}
	wantNote := fx.Notification
	if langMode != "" {
		wantNote = fx.NotificationEN
	}
	assertNotes(t, *notes, wantNote)
	if langMode != "" && wantNote != nil && !strings.Contains(wantNote.Body, "generated message") {
		t.Fatalf("english notification body = %q", wantNote.Body)
	}
}

func TestNotifyScriptHasNoButtons(t *testing.T) {
	if !strings.Contains(notifyScript, "display notification") {
		t.Fatalf("notifyScript = %q", notifyScript)
	}
	for _, banned := range []string{"display dialog", "buttons", "giving up after"} {
		if strings.Contains(notifyScript, banned) {
			t.Fatalf("notifyScript contains %q: %s", banned, notifyScript)
		}
	}
}

// Group 1 must leave Show Full Output where it already is. Opening it for
// pr / split / changelog is AC-5, which this slice has not unlocked.
func TestShowFullOutputUnchanged(t *testing.T) {
	got := map[string]bool{}
	for _, a := range sourceTreeActions(config.Default().SourceTree) {
		got[a.Params] = a.ShowFullOutput
	}
	want := map[string]bool{
		"review -no-color $REPO":          true,
		"gen $REPO":                       false,
		"pr $REPO":                        false,
		"pr-title $REPO":                  false,
		"review-branch -no-color $REPO":   true,
		"stash-msg $REPO":                 false,
		"explain -file=$FILE -repo=$REPO": true,
		"split -repo=$REPO":               false,
		"changelog -repo=$REPO":           false,
	}
	if len(got) != len(want) {
		t.Fatalf("actions = %#v", got)
	}
	for params, show := range want {
		if got[params] != show {
			t.Fatalf("showFullOutput %s = %v, want %v", params, got[params], show)
		}
	}
}
