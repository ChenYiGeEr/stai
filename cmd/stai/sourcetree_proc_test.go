package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type processFixture struct {
	ID                      string `json:"id"`
	ProcessName             string `json:"process_name"`
	PgrepSourcetreeCommand  string `json:"pgrep_sourcetree_command"`
	PgrepSourcetreeExit     int    `json:"pgrep_sourcetree_exit"`
	PgrepSourceTreeCommand  string `json:"pgrep_source_tree_command"`
	PgrepSourceTreeExit     int    `json:"pgrep_source_tree_exit"`
	SourceTreeOnlyWouldMiss bool   `json:"source_tree_only_would_miss"`
	InstallCommand          string `json:"install_command"`
	InstallExitCode         int    `json:"install_exit_code"`
	InstallStderr           string `json:"install_stderr"`
	DoctorCommand           string `json:"doctor_command"`
	DoctorExitCode          int    `json:"doctor_exit_code"`
	Warning                 string `json:"warning"`
	ExpectWarning           bool   `json:"expect_warning"`
}

func TestInstallDoctorProcessNameFixtures(t *testing.T) {
	ids := []string{
		"install-doctor-process-name-sourcetree",
		"install-doctor-process-name-legacy-SourceTree",
		"install-doctor-process-name-quit",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			var fx processFixture
			readFixture(t, id, &fx)
			if fx.ID != id {
				t.Fatalf("fixture id = %q", fx.ID)
			}
			if fx.InstallCommand != "stai install" || fx.DoctorCommand != "stai doctor" {
				t.Fatalf("commands = %q / %q", fx.InstallCommand, fx.DoctorCommand)
			}
			if fx.PgrepSourcetreeCommand != "pgrep -x Sourcetree" || fx.PgrepSourceTreeCommand != "pgrep -x SourceTree" {
				t.Fatalf("detect commands = %q / %q", fx.PgrepSourcetreeCommand, fx.PgrepSourceTreeCommand)
			}
			runProcessNameFixture(t, fx)
		})
	}
}

func runProcessNameFixture(t *testing.T, fx processFixture) {
	t.Helper()
	if fx.ProcessName != "" {
		startNamedSleep(t, fx.ProcessName)
	}
	stExit, stOut := runPgrep(t, "Sourcetree")
	legacyExit, legacyOut := runPgrep(t, "SourceTree")
	if stExit != fx.PgrepSourcetreeExit {
		t.Fatalf("pgrep -x Sourcetree exit = %d, output %q, want %d", stExit, stOut, fx.PgrepSourcetreeExit)
	}
	if legacyExit != fx.PgrepSourceTreeExit {
		t.Fatalf("pgrep -x SourceTree exit = %d, output %q, want %d", legacyExit, legacyOut, fx.PgrepSourceTreeExit)
	}
	onlySourceTreeMatches := legacyExit == 0
	missedBySourceTreeOnly := fx.ProcessName == "Sourcetree" && stExit == 0 && !onlySourceTreeMatches
	if missedBySourceTreeOnly != fx.SourceTreeOnlyWouldMiss {
		t.Fatalf("source_tree_only_would_miss = %v, pgrep Sourcetree=%d %q SourceTree=%d %q",
			fx.SourceTreeOnlyWouldMiss, stExit, stOut, legacyExit, legacyOut)
	}

	repo := initRepo(t)
	home := isolateHome(t)
	writeGlobalConfig(t, home)
	t.Setenv("STAI_BASE_URL", "http://127.0.0.1:1")
	chdir(t, repo)
	restoreInstall := installSourceTreeActionsFn
	installSourceTreeActionsFn = func(exe string, actions []staiAction) error {
		return os.WriteFile(filepath.Join(repo, "plist-write-attempted"), []byte(exe), 0o644)
	}
	t.Cleanup(func() { installSourceTreeActionsFn = restoreInstall })
	restoreCount := countSourceTreeActionsFn
	countSourceTreeActionsFn = func(string, ...string) (int, error) { return 0, nil }
	t.Cleanup(func() { countSourceTreeActionsFn = restoreCount })
	restoreInteractive := sourceTreeInstallInteractiveFn
	sourceTreeInstallInteractiveFn = func() bool { return true }
	t.Cleanup(func() { sourceTreeInstallInteractiveFn = restoreInteractive })

	installErr := captureStderr(t)
	installCode := runExit(func() { cmdInstall(nil) })
	if installCode != fx.InstallExitCode {
		t.Fatalf("install exit = %d, want %d\nstderr:\n%s", installCode, fx.InstallExitCode, installErr.String())
	}
	if installErr.String() != fx.InstallStderr {
		t.Fatalf("install stderr = %q, want %q", installErr.String(), fx.InstallStderr)
	}
	if fx.ExpectWarning && !strings.Contains(installErr.String(), fx.Warning) {
		t.Fatalf("install stderr missing warning:\n%s", installErr.String())
	}
	if !fx.ExpectWarning && strings.Contains(installErr.String(), fx.Warning) {
		t.Fatalf("install stderr has warning while quit:\n%s", installErr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "plist-write-attempted")); err != nil {
		t.Fatalf("install did not continue to the plist write: %v", err)
	}
	hook, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook), staiHookMarker) {
		t.Fatal("prepare-commit-msg hook was not written")
	}

	doctorOut := captureStdout(t)
	doctorErr := captureStderr(t)
	doctorCode := runExit(func() { cmdDoctor(nil) })
	if doctorCode != fx.DoctorExitCode {
		t.Fatalf("doctor exit = %d, want %d\nstdout:\n%s\nstderr:\n%s",
			doctorCode, fx.DoctorExitCode, doctorOut.String(), doctorErr.String())
	}
	combined := doctorOut.String() + doctorErr.String()
	if fx.ExpectWarning && !strings.Contains(combined, fx.Warning) {
		t.Fatalf("doctor output missing warning:\n%s", combined)
	}
	if !fx.ExpectWarning && strings.Contains(combined, fx.Warning) {
		t.Fatalf("doctor output has warning while quit:\n%s", combined)
	}
}

func TestSourceTreeDetectRejectsInexactNames(t *testing.T) {
	startNamedSleep(t, "sourcetree")
	startNamedSleep(t, "SourcetreeX")
	for _, name := range []string{"Sourcetree", "SourceTree"} {
		code, out := runPgrep(t, name)
		if code == 0 {
			t.Fatalf("pgrep -x %s matched inexact processes: %q", name, out)
		}
	}
	repo := initRepo(t)
	home := isolateHome(t)
	writeGlobalConfig(t, home)
	chdir(t, repo)
	restore := installSourceTreeActionsFn
	installSourceTreeActionsFn = func(string, []staiAction) error {
		return os.WriteFile(filepath.Join(repo, "plist-write-attempted"), []byte("x"), 0o644)
	}
	t.Cleanup(func() { installSourceTreeActionsFn = restore })
	restoreInteractive := sourceTreeInstallInteractiveFn
	sourceTreeInstallInteractiveFn = func() bool { return true }
	t.Cleanup(func() { sourceTreeInstallInteractiveFn = restoreInteractive })

	stderr := captureStderr(t)
	code := runExit(func() { cmdInstall(nil) })
	if code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "正在运行的 SourceTree") {
		t.Fatalf("inexact process names produced the running warning:\n%s", stderr.String())
	}
}

func TestDocsNameBothProcessChecks(t *testing.T) {
	root := moduleRoot(t)
	for _, rel := range []string{"README.md", "README.en.md", "INSTALL.md", "INSTALL.en.md"} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		if !strings.Contains(body, "pgrep -x Sourcetree") || !strings.Contains(body, "pgrep -x SourceTree") {
			t.Errorf("%s does not name both exact pgrep checks", rel)
		}
	}
	zh, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zh), "核对版本为 4.2.20（macOS）") {
		t.Error("AC-8 version sentence in README.md is no longer separately present")
	}
	en, err := os.ReadFile(filepath.Join(root, "README.en.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(en), "check version 4.2.20 (macOS)") || !strings.Contains(string(en), "limited to 4.2.20 (macOS)") {
		t.Error("AC-8 version sentences in README.en.md are no longer separately present")
	}
}

func writeGlobalConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "stai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[global]\nlang = \"zh-CN\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startNamedSleep(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := runPgrep(t, name); code == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %s did not show up in pgrep -x", name)
}

func runPgrep(t *testing.T, name string) (int, string) {
	t.Helper()
	cmd := exec.Command("pgrep", "-x", name)
	out, err := cmd.Output()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	t.Fatal(err)
	return -1, ""
}

type stdoutCapture struct {
	buf  bytes.Buffer
	prev *os.File
	w    *os.File
	once sync.Once
	done chan struct{}
}

func captureStdout(t *testing.T) *stdoutCapture {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cap := &stdoutCapture{prev: os.Stdout, w: w, done: make(chan struct{})}
	os.Stdout = w
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

func (c *stdoutCapture) String() string {
	c.once.Do(func() {
		if c.prev != nil {
			os.Stdout = c.prev
			c.prev = nil
		}
		_ = c.w.Close()
		<-c.done
	})
	return c.buf.String()
}
