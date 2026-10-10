package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=stai", "GIT_AUTHOR_EMAIL=test@example.test",
			"GIT_COMMITTER_NAME=stai", "GIT_COMMITTER_EMAIL=test@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.test")
	run("config", "user.name", "stai")
	return dir
}

func TestStagedDiffDir(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StagedDiffDir(dir); err == nil {
		t.Fatal("expected error for no staged changes")
	}
	exec.Command("git", "-C", dir, "add", "a.txt").Run()
	diff, err := StagedDiffDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) == 0 {
		t.Fatal("expected non-empty staged diff")
	}
}

func TestBranchDiffDir(t *testing.T) {
	dir := initRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Commit on main.
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	run("add", "base.txt")
	run("commit", "-m", "base")

	// Create and switch to a feature branch.
	run("checkout", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644)
	run("add", "feature.txt")
	run("commit", "-m", "feature")

	diff, err := BranchDiffDir(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) == 0 {
		t.Fatal("expected non-empty branch diff")
	}

	// No commits ahead should yield empty diff without error.
	diff, err = BranchDiffDir(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 0 {
		t.Fatalf("expected empty diff when comparing branch to itself, got:\n%s", diff)
	}
}

func TestWorkingTreeDiffDir(t *testing.T) {
	dir := initRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("committed\n"), 0o644)
	run("add", "committed.txt")
	run("commit", "-m", "committed")

	if _, err := WorkingTreeDiffDir(dir); err == nil {
		t.Fatal("expected error for no working tree changes")
	}

	// Modify a tracked file (git diff HEAD covers staged + unstaged tracked).
	os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("changed\n"), 0o644)
	diff, err := WorkingTreeDiffDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) == 0 {
		t.Fatal("expected non-empty working tree diff")
	}
}

func TestLastTagAndLogRange(t *testing.T) {
	dir := initRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	cwd, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(cwd) })

	if _, err := LastTag(); err == nil {
		t.Fatal("expected error when no tags exist")
	}

	os.WriteFile(filepath.Join(dir, "v1.txt"), []byte("v1\n"), 0o644)
	run("add", "v1.txt")
	run("commit", "-m", "first")
	run("tag", "v1.0.0")

	os.WriteFile(filepath.Join(dir, "v2.txt"), []byte("v2\n"), 0o644)
	run("add", "v2.txt")
	run("commit", "-m", "second")

	// LastTag should see v1.0.0 (git describe returns the nearest tag).
	tag, err := LastTag()
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.0.0" {
		t.Errorf("LastTag = %q, want v1.0.0", tag)
	}

	// LogRange should return the commit since the tag.
	out, err := LogRange("v1.0.0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("second")) {
		t.Errorf("LogRange missing commit: %s", out)
	}
	if bytes.Contains(out, []byte("first")) {
		t.Errorf("LogRange should not include commits before the tag: %s", out)
	}

	// LogRange should reject an empty until ref.
	if _, err := LogRange("v1.0.0", ""); err == nil {
		t.Fatal("expected error for empty until")
	}

	// Dir variants should work without changing cwd.
	cwd2, _ := os.Getwd()
	tag2, err := LastTagDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tag2 != "v1.0.0" {
		t.Errorf("LastTagDir = %q, want v1.0.0", tag2)
	}
	out2, err := LogRangeDir("v1.0.0", "HEAD", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out2, []byte("second")) {
		t.Errorf("LogRangeDir missing commit: %s", out2)
	}
	if after, _ := os.Getwd(); after != cwd2 {
		t.Errorf("LogRangeDir changed cwd: %s", after)
	}
}
