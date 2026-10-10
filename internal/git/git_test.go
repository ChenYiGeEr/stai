package git

import (
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
