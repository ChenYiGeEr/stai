// Command stai is the AI companion for SourceTree git workflows.
//
// It is not a SourceTree plugin (SourceTree has no plugin API); it hooks into
// git itself — hooks, mergetool wrappers, and SourceTree "custom actions" —
// so everything also works with any other git client.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "gen":
		cmdGen(args)
	case "pr":
		cmdPR(args)
	case "review-branch":
		cmdReviewBranch(args)
	case "stash-msg":
		cmdStashMsg(args)
	case "hook":
		cmdHook(args)
	case "review":
		cmdReview(args)
	case "mergetool":
		cmdMergetool(args)
	case "install":
		cmdInstall(args)
	case "uninstall":
		cmdUninstall(args)
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
  gen          Generate a commit message from staged changes and copy it
               to the clipboard (M1)
  hook         Entrypoint for git hooks:
               stai hook prepare-commit-msg <msg-file> [source]
               stai hook pre-commit
               stai hook pre-push
  pr           Generate a PR description from the branch diff and copy it
               to the clipboard (M3-A)
  review       AI review of the staged changes (M2). Advisory by default;
               -strict exits non-zero on high-severity findings.
               Optional positional argument: the repository path.
  review-branch AI review of the current branch against its base ref (M3-B).
               Advisory by default; -strict exits non-zero.
  stash-msg    Generate a stash message from working-tree changes and copy
               it to the clipboard (M3-C)
  mergetool    Resolve conflicts with per-hunk AI suggestions (M3)
  install      Write the git hooks and register the SourceTree custom actions
  uninstall    Remove everything install wrote (stai-owned hooks and
               custom actions only)

Config: ~/.config/stai/config.toml + per-repo .stai.toml
Env overrides: STAI_BASE_URL, STAI_API_KEY, STAI_MODEL
Disable hooks without uninstalling: STAI_DISABLE=1
`)
}

// cmdGen implements "stai gen": generate a commit message for the staged
// diff and copy it to the clipboard, then surface it in a macOS
// notification — SourceTree custom actions discard stdout, so without the
// notification the user would see nothing before pasting. The paste into
// the commit box is a deliberate user action (Cmd+V), which replaces the
// old dialog's confirm step. With -edit, the message first shows in an
// editable macOS dialog; the edited text is what gets copied. An optional
// positional argument is the repository path (SourceTree passes $REPO).
func cmdGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	editFlag := fs.Bool("edit", false, "show the message in an editable dialog before copying")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	logPath = cfg.Log.Path
	logf("gen start args=%q model=%s base_url=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL)

	// SourceTree custom actions append $REPO (the repository path) to the
	// parameters; the plain CLI passes nothing and uses the current dir.
	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		repoDir, err = os.Getwd()
		fatal(err)
	}
	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)

	msg, err := generate(context.Background(), cfg, diff)
	fatal(err)

	if *editFlag {
		edited, ok := dialogEdit(msg)
		if !ok {
			return // user cancelled: copy nothing
		}
		msg = edited
	}
	// The clipboard is the delivery channel; the notification is the only
	// visible feedback because SourceTree custom actions discard stdout.
	logf("gen repo=%s message=%q", repoDir, msg)
	if err := writeClipboard(msg); err != nil {
		logf("gen clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, "stai: clipboard copy failed: %v\n", err)
		return
	}
	notifyCopied(cfg.Notify, msg)
	logClipboard("exit", msg)
	fmt.Fprintln(os.Stderr, "copied to clipboard")
	fmt.Println(msg)
}

// generate asks the configured model for a commit message for diff. Both
// gen and the hook use it so the provider and rules are wired in one place.
func generate(ctx context.Context, cfg config.Config, diff []byte) (string, error) {
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature
	return client.GenerateCommit(ctx, cfg.Commit, diff)
}

// hookPrePush implements the strict-mode pre-push gate (M3-B). It only
// acts when pre_push.strict is enabled: the branch diff against
// pre_push.base_ref is reviewed and the push is blocked (exit 1) when a
// high-severity finding is present. AI or parse failures never block a push.
func hookPrePush() {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	logPath = cfg.Log.Path
	logf("hook pre-push start strict=%v base=%s", cfg.PrePush.Strict, cfg.PrePush.BaseRef)
	if !cfg.PrePush.Strict {
		return // advisory branch review happens on demand via "stai review-branch"
	}

	diff, err := git.BranchDiffDir(".", cfg.PrePush.BaseRef)
	if err != nil {
		logf("hook pre-push: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook pre-push: no commits ahead of %s", cfg.PrePush.BaseRef)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("hook pre-push: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: branch review failed, pushing as-is: %v\n", err)
		return
	}
	logf("hook pre-push done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, "stai: pre-push 审查发现高危问题,push 已阻断(绕过:STAI_DISABLE=1 或 git push --no-verify):")
		fmt.Fprint(os.Stderr, renderFindings(findings, true))
		os.Exit(1)
	}
}

// cmdPR implements "stai pr": generate a markdown PR description for the
// current branch against pre_push.base_ref and copy it to the clipboard.
func cmdPR(args []string) {
	fs := flag.NewFlagSet("pr", flag.ExitOnError)
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	logPath = cfg.Log.Path

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	logf("pr start args=%q model=%s base_url=%s base=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL, baseRef)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, "stai: 当前分支没有领先 %s 的提交\n", baseRef)
		return
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	desc, err := client.GeneratePR(ctx, cfg.Commit, diff)
	fatal(err)

	logf("pr repo=%s base=%s", repoDir, baseRef)
	if err := writeClipboard(desc); err != nil {
		logf("pr clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, "stai: clipboard copy failed: %v\n", err)
		return
	}
	notify(cfg.Notify.Title, "PR 描述已复制到剪贴板", desc)
	logClipboard("exit", desc)
	fmt.Fprintln(os.Stderr, "copied to clipboard")
	fmt.Println(desc)
}

// cmdReviewBranch implements "stai review-branch": AI review of the branch
// diff against pre_push.base_ref (M3-B). Advisory by default; -strict makes
// it exit non-zero on high-severity findings.
func cmdReviewBranch(args []string) {
	fs := flag.NewFlagSet("review-branch", flag.ExitOnError)
	strictFlag := fs.Bool("strict", false, "exit non-zero on high-severity findings")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	logPath = cfg.Log.Path

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	reviewBaseURL, _, reviewModel, _ := reviewProvider(cfg)
	logf("review-branch start args=%q model=%s base_url=%s base=%s", args, reviewModel, reviewBaseURL, baseRef)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, "stai: 当前分支没有领先 %s 的提交\n", baseRef)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("review-branch error: %v", err)
		fatal(err)
	}
	logf("review-branch done findings=%d high=%v", len(findings), ai.HasHigh(findings))

	report := renderFindings(findings, !*noColorFlag)
	fmt.Print(report)

	// Notifications must stay plain-text (no ANSI).
	plainReport := renderFindings(findings, false)
	notifBody := plainReport
	subtitle := "分支审查完成"
	if len(findings) > cfg.Review.NotifyMaxFindings {
		if path, werr := writeReportFile(cfg, repoDir, findings); werr == nil {
			notifBody = fmt.Sprintf("问题较多,完整报告已写入 %s\n\n%s", path, severitySummary(findings))
			subtitle = "分支审查完成,报告已写入文件"
		} else {
			logf("review-branch report file write failed: %v", werr)
			notifBody = severitySummary(findings)
		}
	}
	notify(cfg.Notify.Title, subtitle, notifBody)

	if (*strictFlag || cfg.PrePush.Strict) && ai.HasHigh(findings) {
		logf("review-branch blocked push (strict, high severity)")
		os.Exit(1)
	}
}

// cmdStashMsg implements "stai stash-msg": generate a stash message from
// the working tree diff (staged + unstaged) and copy it to the clipboard.
func cmdStashMsg(args []string) {
	fs := flag.NewFlagSet("stash-msg", flag.ExitOnError)
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	logPath = cfg.Log.Path

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		repoDir, err = os.Getwd()
		fatal(err)
	}

	logf("stash-msg start args=%q model=%s base_url=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL)

	diff, err := git.WorkingTreeDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: 工作树没有改动")
		return
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := client.GenerateStashMsg(ctx, cfg.Commit, diff)
	fatal(err)

	logf("stash-msg repo=%s message=%q", repoDir, msg)
	if err := writeClipboard(msg); err != nil {
		logf("stash-msg clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, "stai: clipboard copy failed: %v\n", err)
		return
	}
	notify(cfg.Notify.Title, "stash 信息已复制到剪贴板", msg)
	logClipboard("exit", msg)
	fmt.Fprintln(os.Stderr, "copied to clipboard")
	fmt.Println(msg)
}

// dialogEdit shows the generated message in an editable macOS dialog. It
// returns the edited text, or ok=false when the user cancels or the dialog
// fails (never block the workflow because of a UI hiccup).
func dialogEdit(msg string) (edited string, ok bool) {
	script := `on run argv
	return text returned of (display dialog "可编辑,确定后复制到剪贴板,再到 SourceTree 提交框粘贴" default answer (item 1 of argv) buttons {"取消", "确定"} default button "确定" cancel button "取消" with title "stai — 编辑提交信息")
end run`
	cmd := utf8Cmd("osascript", "-e", script, "--", msg)
	out, err := cmd.Output()
	if err != nil {
		return "", false // -128 = user cancelled
	}
	return strings.TrimRight(string(out), "\n"), true
}

// cmdHook is the entrypoint git hooks call. prepare-commit-msg fills in a
// generated message when the commit box is left empty (never blocks); the
// strict-mode pre-commit gate blocks only on high-severity findings when
// review.strict is enabled.
func cmdHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: stai hook <prepare-commit-msg <msg-file> [source] | pre-commit | pre-push>")
		os.Exit(2)
	}
	switch fs.Arg(0) {
	case "prepare-commit-msg":
		hookPrepareCommitMsg(fs.Arg(1), fs.Arg(2))
	case "pre-commit":
		hookPreCommit()
	case "pre-push":
		hookPrePush()
	default:
		fmt.Fprintf(os.Stderr, "unknown hook: %s\n", fs.Arg(0))
		os.Exit(2)
	}
}

// hookPrepareCommitMsg fills in a generated message only when the user left
// the commit message empty. It must never block a commit, so every failure
// path exits 0.
func hookPrepareCommitMsg(msgFile, source string) {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	// Merge and squash commits carry their own messages; never touch those.
	if source == "merge" || source == "squash" || source == "commit" {
		return
	}
	if msgFile == "" {
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
	logPath = cfg.Log.Path
	logf("hook prepare-commit-msg start (message empty)")

	diff, err := git.StagedDiff()
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := generate(ctx, cfg, diff)
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: commit message generation failed, committing as-is: %v\n", err)
		return
	}
	logf("hook prepare-commit-msg message=%q", msg)
	if err := os.WriteFile(msgFile, []byte(msg+"\n"), 0o644); err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
	}
}

// hookPreCommit implements the strict-mode pre-commit gate (M2). It only
// acts when review.strict is enabled: the staged diff is reviewed and the
// commit is blocked (exit 1) when a high-severity finding is present, with
// the report on stderr (git/SourceTree show it on the failed commit).
// Everything else passes: no review without strict, and AI or parse
// failures never block a commit.
func hookPreCommit() {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	logPath = cfg.Log.Path
	logf("hook pre-commit start strict=%v", cfg.Review.Strict)
	if !cfg.Review.Strict {
		return // advisory review happens on demand via "stai review"
	}

	diff, err := git.StagedDiff()
	if err != nil {
		logf("hook pre-commit: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("hook pre-commit: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: review failed, committing as-is: %v\n", err)
		return
	}
	logf("hook pre-commit done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, "stai: pre-commit 审查发现高危问题,提交已阻断(绕过:STAI_DISABLE=1 或 git commit --no-verify):")
		fmt.Fprint(os.Stderr, renderFindings(findings, true))
		os.Exit(1)
	}
}

// cmdInstall writes the prepare-commit-msg, pre-commit, and pre-push hooks
// into the current repository and registers the SourceTree custom actions
// (gen, review, pr, review-branch, stash-msg). Idempotent: stai-owned hooks
// and actions are replaced, everything else is left alone.
func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	noSourceTree := fs.Bool("no-sourcetree", false, "skip registering the SourceTree custom actions")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	exe, err := os.Executable()
	fatal(err)
	exe, err = filepathEvalSymlinks(exe)
	fatal(err)

	hooks := []struct{ name, body string }{
		{
			"prepare-commit-msg",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — fills in an AI-generated message when the commit
# message is left empty. Never blocks a commit.
"%s" hook prepare-commit-msg "$@" || exit 0
`, exe),
		},
		{
			"pre-commit",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — strict-mode pre-commit review (see review.strict).
# Blocks only when stai itself reports high-severity findings (exit 1);
# passes on every other outcome.
"%s" hook pre-commit
status=$?
[ "$status" -eq 1 ] && exit 1
exit 0
`, exe),
		},
		{
			"pre-push",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — strict-mode pre-push review (see pre_push.strict).
# Blocks only when stai itself reports high-severity findings (exit 1);
# passes on every other outcome.
"%s" hook pre-push
status=$?
[ "$status" -eq 1 ] && exit 1
exit 0
`, exe),
		},
	}
	for _, h := range hooks {
		path, err := git.HookPath(h.name)
		fatal(err)
		if err := os.WriteFile(path, []byte(h.body), 0o755); err != nil {
			fatal(fmt.Errorf("writing hook: %w", err))
		}
		fmt.Printf("hook installed: %s\n", path)
	}

	if *noSourceTree {
		return
	}
	if err := installSourceTreeActions(exe, cfg.SourceTree); err != nil {
		fmt.Fprintf(os.Stderr, `SourceTree custom actions not registered: %v

Add them manually: SourceTree → Settings → Custom Actions → Add:
  Menu caption:  %s
  Script to run: %s
  Parameters:    gen $REPO
  Menu caption:  %s
  Script to run: %s
  Parameters:    review -no-color $REPO
  Menu caption:  %s
  Script to run: %s
  Parameters:    pr $REPO
  Menu caption:  %s
  Script to run: %s
  Parameters:    review-branch -no-color $REPO
  Menu caption:  %s
  Script to run: %s
  Parameters:    stash-msg $REPO
`, err, cfg.SourceTree.ActionCaption, exe, cfg.SourceTree.ReviewActionCaption, exe,
			cfg.SourceTree.PRActionCaption, exe,
			cfg.SourceTree.ReviewBranchActionCaption, exe,
			cfg.SourceTree.StashMsgActionCaption, exe)
		return
	}
	fmt.Printf("SourceTree custom actions registered: %s, %s, %s, %s, %s (restart SourceTree)\n",
		cfg.SourceTree.ActionCaption, cfg.SourceTree.ReviewActionCaption,
		cfg.SourceTree.PRActionCaption, cfg.SourceTree.ReviewBranchActionCaption,
		cfg.SourceTree.StashMsgActionCaption)
}

// cmdUninstall removes everything install wrote: hooks that carry the
// "installed by stai" marker (user-written hooks of the same name are left
// alone) and the stai custom actions in SourceTree (other tools' entries are
// preserved). Idempotent: absent targets are fine.
func cmdUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	noSourceTree := fs.Bool("no-sourcetree", false, "skip removing the SourceTree custom actions")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	exe, err := os.Executable()
	fatal(err)
	exe, err = filepathEvalSymlinks(exe)
	fatal(err)

	for _, name := range []string{"prepare-commit-msg", "pre-commit", "pre-push"} {
		path, err := git.HookPath(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stai: %v\n", err)
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stai: %v\n", err)
			continue
		}
		if !strings.Contains(string(data), "installed by stai") {
			fmt.Printf("hook left alone (not written by stai): %s\n", path)
			continue
		}
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(os.Stderr, "stai: removing hook: %v\n", err)
			continue
		}
		fmt.Printf("hook removed: %s\n", path)
	}

	if *noSourceTree {
		return
	}
	removed, err := uninstallSourceTreeActions(exe, cfg.SourceTree)
	if err != nil {
		fmt.Fprintf(os.Stderr, `SourceTree custom actions not removed: %v

Remove them manually: SourceTree → Settings → Custom Actions:
  %s
  %s
  %s
  %s
  %s
`, err, cfg.SourceTree.ActionCaption, cfg.SourceTree.ReviewActionCaption,
			cfg.SourceTree.PRActionCaption, cfg.SourceTree.ReviewBranchActionCaption,
			cfg.SourceTree.StashMsgActionCaption)
		return
	}
	fmt.Printf("SourceTree custom actions removed: %d (restart SourceTree)\n", removed)
}

// staiAction is one SourceTree custom action entry to register.
type staiAction struct {
	Caption        string `json:"caption"`
	Params         string `json:"params"`
	KeyCode        int    `json:"keyCode"`
	Modifiers      int    `json:"modifiers"`
	Display        string `json:"display"`
	ShowFullOutput bool   `json:"showFullOutput"`
}

// installSourceTreeActions registers the custom actions in SourceTree's real
// action storage: ~/Library/Application Support/SourceTree/actions.plist,
// an NSKeyedArchiver plist of mutable dictionaries. The schema was
// captured from a Sourcetree 4.2.19-generated entry (verified at runtime:
// adding an action in the UI rewrites exactly this file). The defaults
// "customActions" key is a legacy migration path and no longer feeds the
// UI. Read-modify-write runs in one JXA script so a crash midway cannot
// corrupt the file; entries owned by other tools are preserved.
func installSourceTreeActions(exe string, st config.SourceTree) error {
	data, err := json.Marshal([]staiAction{
		{st.ReviewActionCaption, "review -no-color $REPO", st.ReviewShortcutKeyCode, st.ReviewShortcutModifiers, st.ReviewShortcutDisplay, true},
		{st.ActionCaption, "gen $REPO", st.ShortcutKeyCode, st.ShortcutModifiers, st.ShortcutDisplay, false},
		{st.PRActionCaption, "pr $REPO", st.PRShortcutKeyCode, st.PRShortcutModifiers, st.PRShortcutDisplay, false},
		{st.ReviewBranchActionCaption, "review-branch -no-color $REPO", st.ReviewBranchShortcutKeyCode, st.ReviewBranchShortcutModifiers, st.ReviewBranchShortcutDisplay, true},
		{st.StashMsgActionCaption, "stash-msg $REPO", st.StashMsgShortcutKeyCode, st.StashMsgShortcutModifiers, st.StashMsgShortcutDisplay, false},
	})
	if err != nil {
		return err
	}
	cmd := utf8Cmd("osascript", "-l", "JavaScript", "-e", actionsPlistJXA,
		"--", exe, string(data))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("updating actions.plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// uninstallSourceTreeActions removes the stai entries (matching the exe path
// or the configured captions) from actions.plist and returns how many were
// removed. Other tools' entries are preserved.
func uninstallSourceTreeActions(exe string, st config.SourceTree) (int, error) {
	captions, err := json.Marshal([]string{st.ActionCaption, st.ReviewActionCaption, st.PRActionCaption, st.ReviewBranchActionCaption, st.StashMsgActionCaption})
	if err != nil {
		return 0, err
	}
	cmd := utf8Cmd("osascript", "-l", "JavaScript", "-e", removeActionsPlistJXA,
		"--", exe, string(captions))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("updating actions.plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("unexpected JXA output: %q", string(out))
	}
	return n, nil
}

// actionsPlistJXA deduplicates and appends the stai entries in actions.plist.
// argv: exe path, JSON array of actions (caption, params — $REPO is expanded
// by SourceTree to the repository path at run time — keyCode, modifiers,
// display text). The actions run directly — no dialog — because review and
// edits happen in SourceTree itself.
const actionsPlistJXA = `function run(argv) {
	ObjC.import('Foundation');
	var exe = argv[0];
	var actions = JSON.parse(argv[1]);
	var path = $.NSHomeDirectory().stringByAppendingPathComponent('Library/Application Support/SourceTree/actions.plist');
	var list = $.NSMutableArray.alloc.init;
	var fm = $.NSFileManager.defaultManager;
	if (fm.fileExistsAtPath(path)) {
		var data = $.NSData.dataWithContentsOfFile(path);
		if (data && data.length > 0) {
			var obj = null;
			try {
				obj = $.NSKeyedUnarchiver.unarchiveObjectWithData(data);
				// A file that exists but does not unarchive to an action
				// list is corrupt or foreign: abort rather than overwrite
				// other tools' entries with an empty list.
				if (!(obj && obj.isKindOfClass && obj.isKindOfClass($.NSArray.class))) throw 'unreadable';
				list = $.NSMutableArray.arrayWithArray(obj);
			} catch (e) {
				throw Error('actions.plist exists but is not a readable action list — leaving it untouched: ' + path);
			}
		}
	}
	var kept = $.NSMutableArray.alloc.init;
	for (var i = 0; i < list.count; i++) {
		var d = list.objectAtIndex(i);
		var t = d.objectForKey('target');
		var n = d.objectForKey('name');
		if (t && t.isEqualToString(exe)) continue;
		var drop = false;
		if (n) {
			for (var j = 0; j < actions.length; j++) {
				if (n.isEqualToString(actions[j].caption)) { drop = true; break; }
			}
		}
		if (drop) continue;
		kept.addObject(d);
	}
	for (var i = 0; i < actions.length; i++) {
		var a = actions[i];
		var e = $.NSMutableDictionary.alloc.init;
		e.setObjectForKey(a.caption, 'name');
		e.setObjectForKey(exe, 'target');
		e.setObjectForKey(a.params, 'params');
		e.setObjectForKey($.NSNumber.numberWithBool(false), 'fileAction');
		e.setObjectForKey($.NSNumber.numberWithBool(false), 'logAction');
		e.setObjectForKey($.NSNumber.numberWithBool(false), 'separateWindow');
		e.setObjectForKey($.NSNumber.numberWithBool(a.showFullOutput), 'showFullOutput');
		e.setObjectForKey($.NSNumber.numberWithInt(0), 'repoAction');
		e.setObjectForKey($.NSNumber.numberWithInt(a.keyCode), 'shortcutKeyCode');
		e.setObjectForKey($.NSNumber.numberWithInt(a.modifiers), 'shortcutKeyModifiers');
		e.setObjectForKey(a.display, 'shortcutKeyDisplay');
		kept.addObject(e);
	}
	var out = $.NSKeyedArchiver.archivedDataWithRootObject(kept);
	if (!out.writeToFileAtomically(path, true)) throw Error('write failed: ' + path);
	return 'actions.plist now has ' + kept.count + ' entries';
}`

// removeActionsPlistJXA drops the stai entries from actions.plist and prints
// the removed count. argv: exe path, JSON array of captions.
const removeActionsPlistJXA = `function run(argv) {
	ObjC.import('Foundation');
	var exe = argv[0];
	var captions = JSON.parse(argv[1]);
	var path = $.NSHomeDirectory().stringByAppendingPathComponent('Library/Application Support/SourceTree/actions.plist');
	var list = $.NSMutableArray.alloc.init;
	var fm = $.NSFileManager.defaultManager;
	if (fm.fileExistsAtPath(path)) {
		var data = $.NSData.dataWithContentsOfFile(path);
		if (data && data.length > 0) {
			var obj = null;
			try {
				obj = $.NSKeyedUnarchiver.unarchiveObjectWithData(data);
				// A file that exists but does not unarchive to an action
				// list is corrupt or foreign: abort rather than overwrite
				// other tools' entries with an empty list.
				if (!(obj && obj.isKindOfClass && obj.isKindOfClass($.NSArray.class))) throw 'unreadable';
				list = $.NSMutableArray.arrayWithArray(obj);
			} catch (e) {
				throw Error('actions.plist exists but is not a readable action list — leaving it untouched: ' + path);
			}
		}
	}
	var kept = $.NSMutableArray.alloc.init;
	var removed = 0;
	for (var i = 0; i < list.count; i++) {
		var d = list.objectAtIndex(i);
		var t = d.objectForKey('target');
		var n = d.objectForKey('name');
		var drop = (t && t.isEqualToString(exe));
		if (!drop && n) {
			for (var j = 0; j < captions.length; j++) {
				if (n.isEqualToString(captions[j])) { drop = true; break; }
			}
		}
		if (drop) { removed++; continue; }
		kept.addObject(d);
	}
	// Nothing to remove from a machine that has no actions.plist: do not
	// create the file by writing an empty list back.
	if (!fm.fileExistsAtPath(path)) return '0';
	var out = $.NSKeyedArchiver.archivedDataWithRootObject(kept);
	if (!out.writeToFileAtomically(path, true)) throw Error('write failed: ' + path);
	return '' + removed;
}`

// writeClipboard puts msg on the pasteboard and verifies it by reading it
// back. pbcopy is tried first; if it reports success but the pasteboard does
// not hold msg (seen when stai runs under SourceTree), the message is written
// through NSPasteboard via osascript. Every step is logged.
func writeClipboard(msg string) error {
	before, berr := pasteboardChangeCount()
	logf("gen clipboard before-write changeCount=%s err=%v", before, berr)

	var stderr bytes.Buffer
	cmd := utf8Cmd("pbcopy")
	cmd.Stdin = strings.NewReader(msg)
	cmd.Stderr = &stderr
	perr := cmd.Run()
	logf("gen clipboard pbcopy err=%v stderr=%q", perr, stderr.String())

	if got, _ := clipboardText(); got == msg {
		logf("gen clipboard written via pbcopy")
		return nil
	}

	cmd = utf8Cmd("osascript", "-l", "JavaScript", "-e", pasteboardWriteJXA, "--", msg)
	stderr.Reset()
	cmd.Stderr = &stderr
	oerr := cmd.Run()
	logf("gen clipboard NSPasteboard fallback err=%v stderr=%q", oerr, stderr.String())

	got, _ := clipboardText()
	if got != msg {
		return fmt.Errorf("pasteboard does not hold the message after both pbcopy and NSPasteboard (pbcopy err=%v, fallback err=%v)", perr, oerr)
	}
	logf("gen clipboard written via NSPasteboard fallback")
	return nil
}

// pasteboardWriteJXA clears the general pasteboard and sets argv[0] as its
// plain-text content.
const pasteboardWriteJXA = `function run(argv) {
	ObjC.import('AppKit');
	var pb = $.NSPasteboard.generalPasteboard;
	pb.clearContents;
	pb.setStringForType($(argv[0]), $.NSPasteboardTypeString);
	return 'ok';
}`

// notify shows a macOS notification. Best-effort: a failure must never
// break the workflow.
func notify(title, subtitle, body string) {
	script := `on run argv
	display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv)
end run`
	cmd := utf8Cmd("osascript", "-e", script, "--", body, title, subtitle)
	_ = cmd.Run()
}

// notifyCopied surfaces the generated message in a macOS notification —
// SourceTree custom actions swallow stdout, so without it the user would
// see nothing before pasting.
func notifyCopied(n config.Notify, msg string) {
	notify(n.Title, n.Subtitle, msg)
}

func filepathEvalSymlinks(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	return p, nil
}

// cmdReview implements "stai review": AI review of the staged diff (M2).
// Advisory by default; -strict (or review.strict in the config) makes it
// exit non-zero when a high-severity finding is present. The report goes to
// stdout; when triggered from SourceTree (stdout discarded) a notification
// carries either the full report (few findings) or a summary plus the
// report-file path.
func cmdReview(args []string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	strictFlag := fs.Bool("strict", false, "exit non-zero on high-severity findings (pre-commit mode)")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)
	logPath = cfg.Log.Path

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		repoDir, err = os.Getwd()
		fatal(err)
	}
	reviewBaseURL, _, reviewModel, _ := reviewProvider(cfg)
	logf("review start args=%q model=%s base_url=%s", args, reviewModel, reviewBaseURL)

	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("review error: %v", err)
		fatal(err)
	}
	logf("review done findings=%d high=%v", len(findings), ai.HasHigh(findings))

	report := renderFindings(findings, !*noColorFlag)
	fmt.Print(report)

	// SourceTree discards stdout, so the report also goes to a
	// notification: the full text when it fits, otherwise a summary plus
	// the report-file path. Notifications must stay plain-text (no ANSI).
	plainReport := renderFindings(findings, false)
	notifBody := plainReport
	subtitle := "审查完成"
	if len(findings) > cfg.Review.NotifyMaxFindings {
		if path, werr := writeReportFile(cfg, repoDir, findings); werr == nil {
			notifBody = fmt.Sprintf("问题较多,完整报告已写入 %s\n\n%s", path, severitySummary(findings))
			subtitle = "审查完成,报告已写入文件"
		} else {
			logf("review report file write failed: %v", werr)
			notifBody = severitySummary(findings)
		}
	}
	notify(cfg.Notify.Title, subtitle, notifBody)

	if (*strictFlag || cfg.Review.Strict) && ai.HasHigh(findings) {
		logf("review blocked commit (strict, high severity)")
		os.Exit(1)
	}
}

// reviewProvider resolves the effective endpoint for review: the review.*
// config overrides win over the global [provider], so review can use a
// stronger model while gen stays on a fast one.
func reviewProvider(cfg config.Config) (baseURL, apiKey, model string, temperature float64) {
	baseURL, apiKey, model = cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model
	temperature = cfg.Provider.Temperature
	if cfg.Review.BaseURL != "" {
		baseURL = cfg.Review.BaseURL
	}
	if cfg.Review.APIKey != "" {
		apiKey = cfg.Review.APIKey
	}
	if cfg.Review.Model != "" {
		model = cfg.Review.Model
	}
	if cfg.Review.Temperature != nil {
		temperature = *cfg.Review.Temperature
	}
	return baseURL, apiKey, model, temperature
}

// generateReview asks the configured model to review diff. Both the review
// command and the pre-commit hook use it so provider wiring lives in one place.
func generateReview(ctx context.Context, cfg config.Config, diff []byte) ([]ai.Finding, error) {
	baseURL, apiKey, model, temperature := reviewProvider(cfg)
	client := ai.NewClient(baseURL, apiKey, model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = temperature
	return client.GenerateReview(ctx, ai.ReviewParams{
		Diff:          diff,
		MaxChars:      cfg.Commit.MaxDiffChars,
		Retries:       cfg.Commit.Retries,
		GroupMaxLines: cfg.Review.GroupMaxLines,
		Rules:         cfg.Review.Rules,
		Concurrency:   cfg.Review.Concurrency,
		Logf:          logf,
	})
}

var severityColor = map[string]string{
	"high":   "\033[31m", // red
	"medium": "\033[33m", // yellow
	"low":    "\033[36m", // cyan
	"":       "\033[90m", // gray for advisory
}

const colorReset = "\033[0m"

// renderFindings formats findings as a readable report; empty means OK.
// When color is true, severity labels get ANSI color codes (suitable for
// terminal and SourceTree's full-output window).
func renderFindings(findings []ai.Finding, color bool) string {
	if len(findings) == 0 {
		return "OK 未发现问题\n"
	}
	var b strings.Builder
	for _, f := range findings {
		sev := strings.ToUpper(f.Severity)
		if sev == "" {
			sev = "?"
		}
		prefix := fmt.Sprintf("[%s]", sev)
		if color {
			if c := severityColor[f.Severity]; c != "" {
				prefix = c + prefix + colorReset
			}
		}
		if f.Severity == "" {
			fmt.Fprintf(&b, "%s %s\n", prefix, f.Message)
		} else if f.Location != "" {
			fmt.Fprintf(&b, "%s %s - %s\n", prefix, f.Location, f.Message)
		} else {
			fmt.Fprintf(&b, "%s %s\n", prefix, f.Message)
		}
	}
	return b.String()
}

// severitySummary counts findings per severity level.
func severitySummary(findings []ai.Finding) string {
	var high, medium, low, other int
	for _, f := range findings {
		switch f.Severity {
		case "high":
			high++
		case "medium":
			medium++
		case "low":
			low++
		default:
			other++
		}
	}
	return fmt.Sprintf("high %d,medium %d,low %d,未归类 %d", high, medium, low, other)
}

// writeReportFile saves the full report under the repo (default
// .git/stai-review.md, configurable via review.report_path) and returns the
// written path.
func writeReportFile(cfg config.Config, repoDir string, findings []ai.Finding) (string, error) {
	path := cfg.Review.ReportPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf("# stai 审查报告\n\n%s\n%s\n",
		time.Now().Format("2006-01-02 15:04:05"), renderFindings(findings, false))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
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
		logf("error: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		os.Exit(1)
	}
}

// logPath is the diagnostic log file: the default until the config is
// loaded, then [log] path from the config.
var logPath = config.Default().Log.Path

// logf appends a timestamped line to the log file. SourceTree discards
// custom-action output, so this is the only record of why a run failed.
// Logging is best-effort and never affects the exit path.
func logf(format string, args ...any) {
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// utf8Cmd builds a command that runs with a UTF-8 locale. SourceTree starts
// custom actions without one: pbcopy then writes an empty pasteboard, and
// pbpaste and osascript mangle non-ASCII text, so stai's own read-back check
// would fail even when the write was fine.
func utf8Cmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8")
	return cmd
}

func clipboardText() (string, error) {
	out, err := utf8Cmd("pbpaste").Output()
	return string(out), err
}

// pasteboardChangeCount returns the general pasteboard's changeCount, which
// increments on every write by any process. A change between two checks
// means something else wrote to the clipboard in between.
func pasteboardChangeCount() (string, error) {
	out, err := utf8Cmd("osascript", "-l", "JavaScript", "-e",
		"ObjC.import('AppKit'); $.NSPasteboard.generalPasteboard.changeCount").Output()
	return strings.TrimSpace(string(out)), err
}

// logClipboard records whether the clipboard still holds want, and the
// pasteboard changeCount, at a named point of the run. Used to tell whether
// the clipboard was emptied after stai wrote to it.
func logClipboard(tag, want string) {
	got, err := clipboardText()
	count, cerr := pasteboardChangeCount()
	logf("gen clipboard %s match=%v changeCount=%s got=%q err=%v countErr=%v",
		tag, got == want, count, got, err, cerr)
}
