package mergetool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stai/internal/i18n"
)

func TestCleanFileDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	merged := filepath.Join(dir, "merged.txt")
	body := []byte("already resolved\n")
	writeTrio(t, dir, body)
	if err := os.WriteFile(merged, body, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := freeze(t, merged)
	code, out := runRefused(t, Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !bytes.Equal(mustRead(t, merged), body) {
		t.Fatalf("clean file changed: %q", mustRead(t, merged))
	}
	assertFrozen(t, merged, stamp)
	if out != "" {
		t.Fatalf("clean file produced UI %q", out)
	}
}

func TestEOFDoesNotWrite(t *testing.T) {
	dir := writeConflict(t, "<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n")
	merged := filepath.Join(dir, "merged.txt")
	stamp := freeze(t, merged)
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  bytes.NewReader(nil),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{Choice: "merge", Resolution: "SILENT-GUESS\n"}, nil
		},
	})
	got := mustRead(t, merged)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, buf.String())
	}
	if bytes.Contains(got, []byte("SILENT-GUESS")) || !bytes.Contains(got, []byte("<<<<<<<")) {
		t.Fatalf("EOF wrote a resolution:\n%s", got)
	}
	assertFrozen(t, merged, stamp)
}

func TestPreviewDoesNotWrite(t *testing.T) {
	dir := writeConflict(t, "<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n")
	merged := filepath.Join(dir, "merged.txt")
	before := mustRead(t, merged)
	stamp := freeze(t, merged)
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  strings.NewReader("pq"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{Explanation: "preview me", Choice: "merge", Resolution: "PREVIEW-ME\n"}, nil
		},
	})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, buf.String())
	}
	if !bytes.Equal(mustRead(t, merged), before) {
		t.Fatal("preview wrote the file")
	}
	assertFrozen(t, merged, stamp)
	if !strings.Contains(buf.String(), "PREVIEW-ME") {
		t.Fatalf("preview text missing\n%s", buf.String())
	}
}

func TestNoEditorStaysAtPrompt(t *testing.T) {
	t.Setenv("GIT_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	dir := writeConflict(t, "<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n")
	merged := filepath.Join(dir, "merged.txt")
	before := mustRead(t, merged)
	stamp := freeze(t, merged)
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  strings.NewReader("eq"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{Choice: "merge", Resolution: "NO-EDITOR-GUESS\n"}, nil
		},
	})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), i18n.T("mergetool_no_editor")) {
		t.Fatalf("missing editor hint\n%s", buf.String())
	}
	got := mustRead(t, merged)
	if !bytes.Equal(got, before) || bytes.Contains(got, []byte("NO-EDITOR-GUESS")) {
		t.Fatalf("file changed without an editor:\n%s", got)
	}
	assertFrozen(t, merged, stamp)
}

func TestEditorEnvOrder(t *testing.T) {
	dir := writeConflict(t, "<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n")
	gitScript := filepath.Join(dir, "git-ed.sh")
	plainScript := filepath.Join(dir, "plain-ed.sh")
	if err := os.WriteFile(gitScript, []byte("#!/bin/sh\nprintf 'FROM-GIT-EDITOR\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plainScript, []byte("#!/bin/sh\nprintf 'FROM-EDITOR\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_EDITOR", gitScript)
	t.Setenv("VISUAL", plainScript)
	t.Setenv("EDITOR", plainScript)
	merged := filepath.Join(dir, "merged.txt")
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  strings.NewReader("e"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{}, context.DeadlineExceeded
		},
	})
	got := mustRead(t, merged)
	if code != 0 || string(got) != "FROM-GIT-EDITOR\n" {
		t.Fatalf("exit %d file %q\n%s", code, got, buf.String())
	}
}

func TestAcceptOursIgnoresResolutionBody(t *testing.T) {
	dir := writeConflict(t, "<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n")
	merged := filepath.Join(dir, "merged.txt")
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  strings.NewReader("a"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{Explanation: "keep local", Choice: "ours", Resolution: "NOT-THIS\n"}, nil
		},
	})
	got := mustRead(t, merged)
	if code != 0 || string(got) != "OURS\n" {
		t.Fatalf("exit %d file %q\n%s", code, got, buf.String())
	}
	if bytes.Contains(got, []byte("NOT-THIS")) {
		t.Fatal("ours choice wrote the model resolution body")
	}
}

func TestDiff3KeysUseSidesNotBase(t *testing.T) {
	body := "left\n<<<<<<< HEAD\nOURS-A\n||||||| ancestor\nBASE-ONLY\n=======\nTHEIRS-A\n>>>>>>> topic\nmid\n<<<<<<< HEAD\nOURS-B\n||||||| ancestor\nBASE-TWO\n=======\nTHEIRS-B\n>>>>>>> topic\nright\n"
	dir := writeConflict(t, body)
	merged := filepath.Join(dir, "merged.txt")
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
		In:  strings.NewReader("ot"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{Choice: "merge", Resolution: "DO-NOT-USE\n"}, nil
		},
	})
	got := mustRead(t, merged)
	want := []byte("left\nOURS-A\nmid\nTHEIRS-B\nright\n")
	if code != 0 || !bytes.Equal(got, want) {
		t.Fatalf("exit %d\n got %q\nwant %q\n%s", code, got, want, buf.String())
	}
}

func TestAddAddWithMissingBase(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "local.txt")
	remote := filepath.Join(dir, "remote.txt")
	merged := filepath.Join(dir, "merged.txt")
	if err := os.WriteFile(local, []byte("OURS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remote, []byte("THEIRS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(merged, []byte("<<<<<<< HEAD\nOURS\n=======\nTHEIRS\n>>>>>>> topic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code := Run(context.Background(), Options{
		Base: "", Local: local, Remote: remote, Merged: merged,
		In:  strings.NewReader("t"),
		Out: &buf,
		Advise: func(context.Context, Hunk) (Advice, error) {
			return Advice{}, os.ErrNotExist
		},
	})
	got := mustRead(t, merged)
	if code != 0 || string(got) != "THEIRS\n" {
		t.Fatalf("exit %d file %q\n%s", code, got, buf.String())
	}
}

func TestMalformedMarkersAreRefused(t *testing.T) {
	dir := writeConflict(t, "<<<<<<< HEAD\nno closer\n")
	merged := filepath.Join(dir, "merged.txt")
	before := mustRead(t, merged)
	stamp := freeze(t, merged)
	code, out := runRefused(t, Options{
		Base: filepath.Join(dir, "base.txt"), Local: filepath.Join(dir, "local.txt"),
		Remote: filepath.Join(dir, "remote.txt"), Merged: merged,
	})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, i18n.T("mergetool_malformed")) {
		t.Fatalf("ui %q", out)
	}
	if !bytes.Equal(mustRead(t, merged), before) {
		t.Fatal("malformed conflict was rewritten")
	}
	assertFrozen(t, merged, stamp)
}

func writeTrio(t *testing.T, dir string, body []byte) {
	t.Helper()
	for _, name := range []string{"base.txt", "local.txt", "remote.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeConflict(t *testing.T, merged string) string {
	t.Helper()
	dir := t.TempDir()
	writeTrio(t, dir, []byte("side\n"))
	if err := os.WriteFile(filepath.Join(dir, "merged.txt"), []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
