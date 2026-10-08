// Command stai is the AI companion for SourceTree git workflows.
//
// It is not a SourceTree plugin (SourceTree has no plugin API); it hooks into
// git itself — hooks, mergetool wrappers, and SourceTree "custom actions" —
// so everything also works with any other git client.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
)

// maxDiffChars caps the diff sent to the model. Beyond this we truncate and
// tell the model, rather than failing the commit.
const maxDiffChars = 60000

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "gen":
		cmdGen(args)
	case "hook":
		cmdHook(args)
	case "review":
		cmdReview(args)
	case "mergetool":
		cmdMergetool(args)
	case "install":
		cmdInstall(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `stai - AI companion for SourceTree git workflows

Usage:
  stai <command> [flags]

Commands:
  gen        Generate a commit message from staged changes and copy it
             to the clipboard (M1)
  hook       Entrypoint for git hooks:
             stai hook prepare-commit-msg <msg-file> [source]
  review     AI review of the staged changes (M2, advisory by default)
  mergetool  Resolve conflicts with per-hunk AI suggestions (M3)
  install    Write the prepare-commit-msg hook and register the
             SourceTree custom action (M1)

Config: ~/.config/stai/config.toml + per-repo .stai.toml
Env overrides: STAI_BASE_URL, STAI_API_KEY, STAI_MODEL
Disable hooks without uninstalling: STAI_DISABLE=1
`)
}

// cmdGen implements "stai gen": generate a commit message for the staged
// diff and copy it to the clipboard so it can be pasted into SourceTree.
func cmdGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	copyFlag := fs.Bool("copy", true, "copy the generated message to the clipboard")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)

	diff, err := git.StagedDiff()
	fatal(err)

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model)
	msg, err := client.GenerateCommit(context.Background(), ai.CommitParams{
		Style:    cfg.Commit.Style,
		Language: cfg.Commit.Language,
		Diff:     diff,
		MaxChars: maxDiffChars,
	})
	fatal(err)

	if *copyFlag && copyToClipboard(msg) == nil {
		fmt.Fprintln(os.Stderr, "copied to clipboard")
	}
	fmt.Println(msg)
}

// cmdHook implements "stai hook prepare-commit-msg": fill in a generated
// message only when the user left the commit message empty. It must never
// block a commit, so every failure path exits 0.
func cmdHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	fs.Parse(args)
	if len(fs.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "usage: stai hook <prepare-commit-msg> <msg-file> [source]")
		os.Exit(2)
	}
	if fs.Arg(0) != "prepare-commit-msg" {
		fmt.Fprintf(os.Stderr, "unknown hook: %s\n", fs.Arg(0))
		os.Exit(2)
	}

	msgFile := fs.Arg(1)
	source := fs.Arg(2)

	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	// Merge and squash commits carry their own messages; never touch those.
	if source == "merge" || source == "squash" || source == "commit" {
		return
	}
	data, err := os.ReadFile(msgFile)
	if err != nil || strings.TrimSpace(string(data)) != "" {
		return // existing message (or unreadable file): leave it alone
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	diff, err := git.StagedDiff()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model)
	msg, err := client.GenerateCommit(ctx, ai.CommitParams{
		Style:    cfg.Commit.Style,
		Language: cfg.Commit.Language,
		Diff:     diff,
		MaxChars: maxDiffChars,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: commit message generation failed, committing as-is: %v\n", err)
		return
	}
	if err := os.WriteFile(msgFile, []byte(msg+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
	}
}

// cmdInstall writes the prepare-commit-msg hook into the current repository
// and registers the "AI 生成提交信息" custom action in SourceTree.
func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	noSourceTree := fs.Bool("no-sourcetree", false, "skip registering the SourceTree custom action")
	fs.Parse(args)

	exe, err := os.Executable()
	fatal(err)
	exe, err = filepathEvalSymlinks(exe)
	fatal(err)

	hookPath, err := git.HookPath("prepare-commit-msg")
	fatal(err)
	hookScript := fmt.Sprintf(`#!/bin/sh
# installed by stai — fills in an AI-generated message when the commit
# message is left empty. Never blocks a commit.
"%s" hook prepare-commit-msg "$@" || exit 0
`, exe)
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		fatal(fmt.Errorf("writing hook: %w", err))
	}
	fmt.Printf("hook installed: %s\n", hookPath)

	if *noSourceTree {
		return
	}
	if err := installSourceTreeAction(exe); err != nil {
		fmt.Fprintf(os.Stderr, `SourceTree custom action not registered: %v

Add it manually: SourceTree → Settings → Custom Actions → Add:
  Menu caption:  AI 生成提交信息
  Script to run: %s
  Parameters:    gen
Then run "stai gen" in the repo (it copies the message for the commit box).
`, err, exe)
		return
	}
	fmt.Println("SourceTree custom action registered: AI 生成提交信息 (restart SourceTree)")
}

// installSourceTreeAction registers the custom action in SourceTree's
// preferences. The entry schema (name/command/parameters + has*Param flags)
// was recovered from the Sourcetree 4.2.19 binary.
func installSourceTreeAction(exe string) error {
	dict := fmt.Sprintf(`{
		name = "AI 生成提交信息";
		command = "%s";
		parameters = "gen";
		hasFileParam = 0;
		hasRepoParam = 1;
		hasSHAParam = 0;
	}`, exe)
	cmd := exec.Command("defaults", "write", "com.torusknot.SourceTreeNotMAS",
		"customActions", "-array-add", dict)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("defaults write: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyToClipboard(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func filepathEvalSymlinks(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	return p, nil
}

func cmdReview(args []string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	_ = fs.Bool("strict", false, "exit non-zero on high-severity findings (pre-commit mode)")
	fs.Parse(args)
	notImplemented("review", "M2: AI review of staged diff, advisory output by default")
}

func cmdMergetool(args []string) {
	fs := flag.NewFlagSet("mergetool", flag.ExitOnError)
	fs.Parse(args)
	notImplemented("mergetool",
		"M3: wrap git mergetool, per-hunk explanation + recommendation, never auto-write")
}

func notImplemented(cmd, plan string) {
	fmt.Printf("stai %s: not implemented yet\n  plan: %s\n", cmd, plan)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		os.Exit(1)
	}
}
