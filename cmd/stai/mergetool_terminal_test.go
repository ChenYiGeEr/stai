package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stai/internal/i18n"
	"stai/internal/mergetool"
)

func TestFixture_mergetool_no_tty_terminal(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		code, got := relayFixture(t, "resolve")
		wantExit := readExitFile(t, "resolve/exit.txt")
		want := readFixture(t, "resolve/expected.txt")
		if code != wantExit || wantExit != 0 {
			t.Fatalf("exit %d, want %d", code, wantExit)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
		}
		if bytes.Contains(got, []byte("<<<<<<<")) {
			t.Fatal("resolved file still has conflict markers")
		}
	})

	t.Run("quit", func(t *testing.T) {
		code, got := relayFixture(t, "quit")
		wantExit := readExitFile(t, "quit/exit.txt")
		want := readFixture(t, "quit/expected.txt")
		if code != wantExit || wantExit != 1 {
			t.Fatalf("exit %d, want %d", code, wantExit)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("$MERGED mismatch\n got %q\nwant %q", got, want)
		}
		if !bytes.Contains(got, []byte("<<<<<<<")) {
			t.Fatal("quit removed conflict markers")
		}
		if bytes.Contains(got, []byte("FORBIDDEN-REC-Q2")) {
			t.Fatal("quit wrote the recommendation into the unprocessed hunk")
		}
	})

	t.Run("no-terminal", func(t *testing.T) {
		// This VM is not macOS. The real launcher must refuse.
		dir := t.TempDir()
		merged := filepath.Join(dir, "merged.txt")
		original := readFixture(t, "no-terminal/merged.txt")
		if err := os.WriteFile(merged, original, 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := freezeMerged(t, merged)
		var stdout, stderr bytes.Buffer
		code := runMergetool([]string{
			"--base", filepath.Join(dir, "base.txt"),
			"--local", filepath.Join(dir, "local.txt"),
			"--remote", filepath.Join(dir, "remote.txt"),
			"--merged", merged,
		}, bytes.NewReader([]byte("aot\n")), &stdout, &stderr)
		wantExit := readExitFile(t, "no-terminal/exit.txt")
		if code != wantExit || wantExit != 1 {
			t.Fatalf("exit %d, want %d\nstderr %s", code, wantExit, stderr.String())
		}
		got, err := os.ReadFile(merged)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("$MERGED changed without a terminal\n got %q\nwant %q", got, original)
		}
		info, err := os.Stat(merged)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stamp) {
			t.Fatalf("mtime changed without a terminal: %s", info.ModTime())
		}
		reason := i18n.T("mergetool_no_terminal", i18n.T("mergetool_terminal_unavailable"))
		if !strings.Contains(stderr.String(), reason) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), reason)
		}
	})

	t.Run("launcher-error", func(t *testing.T) {
		restore := launchMergetoolTerminal
		t.Cleanup(func() { launchMergetoolTerminal = restore })
		launchMergetoolTerminal = func(context.Context, string, string, string, string) (int, error) {
			return 0, os.ErrNotExist
		}
		dir := t.TempDir()
		merged := filepath.Join(dir, "merged.txt")
		original := readFixture(t, "no-terminal/merged.txt")
		if err := os.WriteFile(merged, original, 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := freezeMerged(t, merged)
		var stderr bytes.Buffer
		code := runMergetool([]string{"--merged", merged}, bytes.NewReader([]byte("aot\n")), &bytes.Buffer{}, &stderr)
		if code != 1 {
			t.Fatalf("exit %d, stderr %s", code, stderr.String())
		}
		if !bytes.Equal(mustReadPath(t, merged), original) {
			t.Fatal("launcher error wrote $MERGED")
		}
		if !mustStat(t, merged).ModTime().Equal(stamp) {
			t.Fatal("launcher error rewrote $MERGED")
		}
		if !strings.Contains(stderr.String(), i18n.T("mergetool_no_terminal", os.ErrNotExist.Error())) {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("child-without-tty", func(t *testing.T) {
		restore := launchMergetoolTerminal
		t.Cleanup(func() { launchMergetoolTerminal = restore })
		called := false
		launchMergetoolTerminal = func(context.Context, string, string, string, string) (int, error) {
			called = true
			return 0, nil
		}
		t.Setenv("STAI_MERGETOOL_TERMINAL", "1")
		dir := t.TempDir()
		merged := filepath.Join(dir, "merged.txt")
		original := readFixture(t, "no-terminal/merged.txt")
		if err := os.WriteFile(merged, original, 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := freezeMerged(t, merged)
		var stderr bytes.Buffer
		code := runMergetool([]string{"--merged", merged}, bytes.NewReader([]byte("aot\n")), &bytes.Buffer{}, &stderr)
		if called {
			t.Fatal("child without a TTY opened another terminal")
		}
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		if !bytes.Equal(mustReadPath(t, merged), original) || !mustStat(t, merged).ModTime().Equal(stamp) {
			t.Fatal("child without a TTY wrote $MERGED")
		}
		if !strings.Contains(stderr.String(), i18n.T("mergetool_terminal_not_tty")) {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("english", func(t *testing.T) {
		prev := i18n.Current()
		i18n.SetLang("en")
		t.Cleanup(func() { i18n.SetLang(prev) })
		dir := t.TempDir()
		merged := filepath.Join(dir, "merged.txt")
		if err := os.WriteFile(merged, readFixture(t, "no-terminal/merged.txt"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := runMergetool([]string{"--merged", merged}, bytes.NewReader(nil), &bytes.Buffer{}, &stderr)
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(stderr.String(), "Terminal.app") || !strings.Contains(stderr.String(), "was not written") {
			t.Fatalf("english reason = %q", stderr.String())
		}
	})
}

func TestMergetoolTerminalCommandRecordsExit(t *testing.T) {
	got := mergetoolTerminalCommand("/opt/stai bin/stai", "/base file", "/local", "/remote", "/merged", "/status file")
	for _, part := range []string{
		"STAI_MERGETOOL_TERMINAL=1",
		shellQuote("/opt/stai bin/stai"),
		"--base", shellQuote("/base file"),
		"--local", shellQuote("/local"),
		"--remote", shellQuote("/remote"),
		"--merged", shellQuote("/merged"),
		`echo "$code" > ` + shellQuote("/status file"),
		`exit "$code"`,
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("command %q missing %s", got, part)
		}
	}
	script := mergetoolCommandScript("/opt/stai bin/stai", "/base file", "/local", "/remote", "/merged", "/status file")
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatalf("script = %q", script)
	}
	if strings.Contains(script, "osascript") || strings.Contains(script, "tell application") {
		t.Fatalf("script still sends Apple events:\n%s", script)
	}
	argv := terminalOpenArgv("/tmp/stai-mergetool.command")
	if strings.Join(argv, " ") != "/usr/bin/open -a Terminal /tmp/stai-mergetool.command" {
		t.Fatalf("open argv = %#v", argv)
	}
	for _, arg := range argv {
		if strings.Contains(arg, "osascript") || strings.Contains(arg, "tell application") {
			t.Fatalf("open argv uses Apple events: %#v", argv)
		}
	}
}

func TestWaitMergetoolStatusRelaysCode(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- waitMergetoolStatus(ctx, status)
	}()
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(status, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("relayed %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not observe the exit code")
	}

	cancel()
	missing := filepath.Join(dir, "absent")
	if code := waitMergetoolStatus(ctx, missing); code != 1 {
		t.Fatalf("cancelled wait returned %d", code)
	}
}

func relayFixture(t *testing.T, name string) (int, []byte) {
	t.Helper()
	restore := launchMergetoolTerminal
	t.Cleanup(func() { launchMergetoolTerminal = restore })

	dir := t.TempDir()
	for _, file := range []string{"base.txt", "local.txt", "remote.txt", "merged.txt"} {
		if err := os.WriteFile(filepath.Join(dir, file), readFixture(t, name+"/"+file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keys := readFixture(t, name+"/keys.txt")
	advice := loadAdviceFixture(t, name+"/advice.json")
	var calls int
	launchMergetoolTerminal = func(_ context.Context, base, local, remote, merged string) (int, error) {
		if base != filepath.Join(dir, "base.txt") || merged != filepath.Join(dir, "merged.txt") ||
			local != filepath.Join(dir, "local.txt") || remote != filepath.Join(dir, "remote.txt") {
			t.Errorf("terminal args base=%s local=%s remote=%s merged=%s", base, local, remote, merged)
		}
		var buf bytes.Buffer
		code := mergetool.Run(context.Background(), mergetool.Options{
			Base: base, Local: local, Remote: remote, Merged: merged,
			In:  bytes.NewReader(keys),
			Out: &buf,
			Advise: func(context.Context, mergetool.Hunk) (mergetool.Advice, error) {
				if calls >= len(advice) {
					t.Errorf("advice overrun")
					return mergetool.Advice{}, os.ErrClosed
				}
				a := advice[calls]
				calls++
				return a, nil
			},
		})
		return code, nil
	}
	merged := filepath.Join(dir, "merged.txt")
	var stdout, stderr bytes.Buffer
	code := runMergetool([]string{
		"--base", filepath.Join(dir, "base.txt"),
		"--local", filepath.Join(dir, "local.txt"),
		"--remote", filepath.Join(dir, "remote.txt"),
		"--merged", merged,
	}, bytes.NewReader([]byte("aot\n")), &stdout, &stderr)
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("parent wrote UI outside the terminal\nstdout %s\nstderr %s", stdout.String(), stderr.String())
	}
	return code, mustReadPath(t, merged)
}

type adviceFixture struct {
	Hunks []mergetool.Advice `json:"hunks"`
}

func loadAdviceFixture(t *testing.T, rel string) []mergetool.Advice {
	t.Helper()
	var file adviceFixture
	if err := json.Unmarshal(readFixture(t, rel), &file); err != nil {
		t.Fatal(err)
	}
	return file.Hunks
}

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mergetool-no-tty-terminal", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readExitFile(t *testing.T, rel string) int {
	t.Helper()
	s := strings.TrimSpace(string(readFixture(t, rel)))
	if s == "0" {
		return 0
	}
	if s == "1" {
		return 1
	}
	t.Fatalf("exit file %s = %q", rel, s)
	return -1
}

func mustReadPath(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func freezeMerged(t *testing.T, path string) time.Time {
	t.Helper()
	ts := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
	return ts
}
