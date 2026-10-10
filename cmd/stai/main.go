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
	"stai/internal/i18n"
)

// loadConfig loads the layered config, wires the output language into both
// the CLI text (i18n) and the AI prompts (ai), and sets the log path.
// Every command uses this instead of calling config.Load directly.
func loadConfig() config.Config {
	cfg, err := config.Load()
	fatal(err)
	setLangFromConfig(cfg)
	if _, ok := i18n.Normalize(cfg.Global.Lang); !ok {
		// Warn after the language switch so the message renders in the
		// active language (for invalid values that is the zh-CN fallback).
		fmt.Fprintf(os.Stderr, i18n.T("lang_fallback")+"\n", cfg.Global.Lang)
	}
	logPath = cfg.Log.Path
	return cfg
}

// setLangFromConfig wires the (already loaded) config language into the i18n
// and ai packages, silently falling back to zh-CN. Silent because hooks use
// it and must never block or nag — the CLI path warns via loadConfig.
func setLangFromConfig(cfg config.Config) {
	lang, _ := i18n.Normalize(cfg.Global.Lang)
	i18n.SetLang(lang)
	ai.SetLang(lang)
}

// initLang wires the configured language before command dispatch, so even
// usage/error text on paths that never load the config (no args, unknown
// command) honors [global] lang and STAI_LANG. Silent: commands that do
// load the config warn there via loadConfig.
func initLang() {
	cfg, err := config.Load()
	if err != nil {
		return // broken config: stay on the zh-CN default
	}
	setLangFromConfig(cfg)
}

func main() {
	initLang()
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
	case "pr-title":
		cmdPRTitle(args)
	case "review-branch":
		cmdReviewBranch(args)
	case "stash-msg":
		cmdStashMsg(args)
	case "explain":
		cmdExplain(args)
	case "split":
		cmdSplit(args)
	case "changelog":
		cmdChangelog(args)
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
		fmt.Fprintf(os.Stderr, i18n.T("unknown_command")+"\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, i18n.T("usage"))
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

	cfg := loadConfig()
	logf("gen start args=%q model=%s base_url=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL)

	// SourceTree custom actions append $REPO (the repository path) to the
	// parameters; the plain CLI passes nothing and uses the current dir.
	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}
	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("staged_empty"))
		os.Exit(1)
	}

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
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notifyCopied(cfg.Notify, msg)
	logClipboard("exit", msg)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
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
	setLangFromConfig(cfg)
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
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("review_failed_push")+"\n", err)
		return
	}
	logf("hook pre-push done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, i18n.T("review_blocked_push"))
		fmt.Fprint(os.Stderr, renderFindings(findings, true, true))
		os.Exit(1)
	}
}

// commandContext derives a timeout context from parent for a CLI command.
// Callers pass context.Background() today — stai is a short-lived CLI process
// with no upstream cancellation — but accepting parent keeps the door open
// for embedding stai commands in a larger cancellable flow.
func commandContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

// cmdPR implements "stai pr": generate a markdown PR description for the
// current branch against pre_push.base_ref and copy it to the clipboard.
func cmdPR(args []string) {
	fs := flag.NewFlagSet("pr", flag.ExitOnError)
	fs.Parse(args)

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	logf("pr start args=%q model=%s base_url=%s base=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL, baseRef)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("ahead_none")+"\n", baseRef)
		return
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature
	client.Logf = logf

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	title, desc, err := client.GeneratePR(ctx, cfg.Commit, diff)
	fatal(err)

	// F1 delivery: the clipboard holds the combined block (title line, blank
	// line, description); the notification subtitle carries the title.
	combined := title + "\n\n" + desc
	logf("pr repo=%s base=%s title=%q", repoDir, baseRef, title)
	if err := writeClipboard(combined); err != nil {
		logf("pr clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, title, desc)
	logClipboard("exit", combined)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(combined)
}

// cmdPRTitle implements "stai pr-title": generate a one-line PR title for
// the current branch against pre_push.base_ref and copy it to the clipboard.
func cmdPRTitle(args []string) {
	fs := flag.NewFlagSet("pr-title", flag.ExitOnError)
	fs.Parse(args)

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, i18n.T("usage_pr_title"))
		os.Exit(2)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	logf("pr-title start args=%q model=%s base_url=%s base=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL, baseRef)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("ahead_none")+"\n", baseRef)
		return
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature
	client.Logf = logf

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	title, err := client.GeneratePRTitle(ctx, cfg.Commit, diff)
	fatal(err)

	logf("pr-title repo=%s base=%s title=%q", repoDir, baseRef, title)
	if err := writeClipboard(title); err != nil {
		logf("pr-title clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, i18n.T("notify_pr_title"), title)
	logClipboard("exit", title)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(title)
}

// cmdReviewBranch implements "stai review-branch": AI review of the branch
// diff against pre_push.base_ref (M3-B). Advisory by default; -strict makes
// it exit non-zero on high-severity findings.
func cmdReviewBranch(args []string) {
	fs := flag.NewFlagSet("review-branch", flag.ExitOnError)
	strictFlag := fs.Bool("strict", false, "exit non-zero on high-severity findings")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	fs.Parse(args)

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	reviewBaseURL, _, reviewModel, _ := reviewProvider(cfg)
	logf("review-branch start args=%q model=%s base_url=%s base=%s", args, reviewModel, reviewBaseURL, baseRef)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("ahead_none")+"\n", baseRef)
		return
	}

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("review-branch error: %v", err)
		fatal(err)
	}
	logf("review-branch done findings=%d high=%v", len(findings), ai.HasHigh(findings))

	report := renderFindings(findings, !*noColorFlag, true)
	fmt.Print(report)

	// Notifications must stay plain-text (no ANSI) and concise (no fixes).
	plainReport := renderFindings(findings, false, false)
	notifBody := plainReport
	subtitle := i18n.T("review_subtitle_done")
	if len(findings) > cfg.Review.NotifyMaxFindings {
		if path, werr := writeReportFile(cfg, repoDir, findings); werr == nil {
			notifBody = fmt.Sprintf(i18n.T("review_report_notice"), path, severitySummary(findings))
			subtitle = i18n.T("review_subtitle_file")
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

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	logf("stash-msg start args=%q model=%s base_url=%s", args, cfg.Provider.Model, cfg.Provider.BaseURL)

	diff, err := git.WorkingTreeDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("worktree_empty"))
		os.Exit(1)
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := client.GenerateStashMsg(ctx, cfg.Commit, diff)
	fatal(err)

	logf("stash-msg repo=%s message=%q", repoDir, msg)
	if err := writeClipboard(msg); err != nil {
		logf("stash-msg clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, i18n.T("notify_stash"), msg)
	logClipboard("exit", msg)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(msg)
}

// cmdExplain implements "stai explain [-file <file>] [<file> [repo]]": explain
// the content of a single file. Designed for SourceTree's $FILE custom-action
// placeholder, which the registered action passes as -file=$FILE.
func cmdExplain(args []string) {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	fileFlag := fs.String("file", "", "file to explain (SourceTree $FILE)")
	repoFlag := fs.String("repo", "", "repository path (overrides positional repo)")
	fs.Parse(args)

	// -file wins (SourceTree action form); positional file is the CLI form.
	filePath := *fileFlag
	if filePath == "" && fs.NArg() > 0 {
		filePath = fs.Arg(0)
	}
	if filePath == "" {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("select_file_first"))
		os.Exit(2)
	}
	repoDir := *repoFlag
	if repoDir == "" && fs.NArg() > 1 {
		repoDir = fs.Arg(1)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		if err != nil {
			fatal(err)
		}
	}

	// Resolve relative paths against the repository root, validate, then read
	// the absolute path directly. No chdir: the file is addressed absolutely
	// and the process cwd must stay untouched.
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(repoDir, filePath)
	}

	cfg := loadConfig()

	logf("explain start file=%q repo=%q model=%s", filePath, repoDir, cfg.Provider.Model)

	if err := validateFileInRepo(filePath, repoDir); err != nil {
		fatal(err)
	}

	content, err := os.ReadFile(filePath)
	fatal(err)
	if len(bytes.TrimSpace(content)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("file_empty")+"\n", filePath)
		return
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	explanation, err := client.GenerateExplanation(ctx, cfg.Commit, filePath, content)
	fatal(err)

	logf("explain file=%q repo=%q", filePath, repoDir)
	if err := writeClipboard(explanation); err != nil {
		logf("explain clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, i18n.T("notify_explain"), filePath)
	logClipboard("exit", explanation)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(explanation)
}

// cmdSplit implements "stai split [repo]": suggest how to split the staged
// diff into logical commits.
func cmdSplit(args []string) {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	repoFlag := fs.String("repo", "", "repository path (overrides positional repo)")
	fs.Parse(args)

	repoDir := *repoFlag
	if repoDir == "" && fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, i18n.T("usage_split"))
		os.Exit(2)
	}
	if repoDir == "" {
		repoDir = "."
	}

	cfg := loadConfig()

	logf("split start args=%q repo=%q model=%s", args, repoDir, cfg.Provider.Model)

	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("staged_empty"))
		os.Exit(1)
	}

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	suggestion, err := client.GenerateSplitSuggestion(ctx, cfg.Commit, diff)
	fatal(err)

	logf("split repo=%s", repoDir)
	if err := writeClipboard(suggestion); err != nil {
		logf("split clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, i18n.T("notify_split"), "")
	logClipboard("exit", suggestion)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(suggestion)
}

// cmdChangelog implements "stai changelog [since]": generate release notes
// from git log. If since is empty, uses the most recent tag.
func cmdChangelog(args []string) {
	fs := flag.NewFlagSet("changelog", flag.ExitOnError)
	repoFlag := fs.String("repo", "", "repository path")
	fs.Parse(args)

	repoDir := *repoFlag
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, i18n.T("usage_changelog"))
		os.Exit(2)
	}
	if repoDir == "" {
		repoDir = "."
	}

	cfg := loadConfig()

	since := ""
	if fs.NArg() > 0 {
		since = fs.Arg(0)
	}
	if since == "" {
		tag, err := git.LastTagDir(repoDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("no_tag")+"\n", err)
			os.Exit(2)
		}
		since = tag
	}

	logf("changelog start since=%q repo=%q model=%s", since, repoDir, cfg.Provider.Model)

	logOut, err := git.LogRangeDir(since, "HEAD", repoDir)
	fatal(err)
	if len(bytes.TrimSpace(logOut)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("log_empty")+"\n", since)
		return
	}
	input := fmt.Sprintf("范围: %s..HEAD\n\n提交历史:\n%s", since, string(logOut))

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	notes, err := client.GenerateChangelog(ctx, cfg.Commit, input)
	fatal(err)

	logf("changelog since=%q", since)
	if err := writeClipboard(notes); err != nil {
		logf("changelog clipboard write failed: %v", err)
		fmt.Fprintf(os.Stderr, i18n.T("clipboard_failed")+"\n", err)
		return
	}
	notify(cfg.Notify.Title, i18n.T("notify_changelog"), since)
	logClipboard("exit", notes)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(notes)
}

// dialogEdit shows the generated message in an editable macOS dialog. It
// returns the edited text, or ok=false when the user cancels or the dialog
// fails (never block the workflow because of a UI hiccup).
func dialogEdit(msg string) (edited string, ok bool) {
	cancel, okBtn := i18n.T("dialog_cancel"), i18n.T("dialog_ok")
	script := fmt.Sprintf(`on run argv
	return text returned of (display dialog %q default answer (item 1 of argv) buttons {%q, %q} default button %q cancel button %q with title %q)
end run`, i18n.T("dialog_edit_body"), cancel, okBtn, okBtn, cancel, i18n.T("dialog_edit_title"))
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
		fmt.Fprintln(os.Stderr, i18n.T("usage_hook"))
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
		fmt.Fprintf(os.Stderr, i18n.T("unknown_hook")+"\n", fs.Arg(0))
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
	setLangFromConfig(cfg)
	logPath = cfg.Log.Path
	logf("hook prepare-commit-msg start (message empty)")

	diff, err := git.StagedDiff()
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook prepare-commit-msg: nothing staged, skipping")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := generate(ctx, cfg, diff)
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("hook_msg_gen_failed")+"\n", err)
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
	setLangFromConfig(cfg)
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
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook pre-commit: nothing staged, passing")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("hook pre-commit: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("review_failed_commit")+"\n", err)
		return
	}
	logf("hook pre-commit done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, i18n.T("review_blocked_commit"))
		fmt.Fprint(os.Stderr, renderFindings(findings, true, true))
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

	cfg := loadConfig()
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
		fmt.Printf(i18n.T("install_hook_installed")+"\n", path)
	}

	if *noSourceTree {
		return
	}
	if err := installSourceTreeActions(exe, cfg.SourceTree); err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("install_actions_failed"), err)
		fmt.Fprintln(os.Stderr, i18n.T("install_add_manually"))
		for _, a := range sourceTreeActions(cfg.SourceTree) {
			fmt.Fprintf(os.Stderr, i18n.T("install_caption")+"\n", a.Caption)
			fmt.Fprintf(os.Stderr, i18n.T("install_script")+"\n", exe)
			fmt.Fprintf(os.Stderr, i18n.T("install_params")+"\n", a.Params)
		}
		return
	}
	captions := make([]string, 0, len(sourceTreeActions(cfg.SourceTree)))
	for _, a := range sourceTreeActions(cfg.SourceTree) {
		captions = append(captions, a.Caption)
	}
	fmt.Printf(i18n.T("install_registered")+"\n", strings.Join(captions, ", "))
}

// cmdUninstall removes everything install wrote: hooks that carry the
// "installed by stai" marker (user-written hooks of the same name are left
// alone) and the stai custom actions in SourceTree (other tools' entries are
// preserved). Idempotent: absent targets are fine.
func cmdUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	noSourceTree := fs.Bool("no-sourcetree", false, "skip removing the SourceTree custom actions")
	fs.Parse(args)

	cfg := loadConfig()
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
			fmt.Printf(i18n.T("install_hook_left_alone")+"\n", path)
			continue
		}
		if err := os.Remove(path); err != nil {
			fmt.Fprintf(os.Stderr, "stai: "+i18n.T("install_removing_failed")+"\n", err)
			continue
		}
		fmt.Printf(i18n.T("install_hook_removed")+"\n", path)
	}

	if *noSourceTree {
		return
	}
	removed, err := uninstallSourceTreeActions(exe, cfg.SourceTree)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("install_remove_failed"), err)
		fmt.Fprintln(os.Stderr, i18n.T("install_remove_manually"))
		for _, a := range sourceTreeActions(cfg.SourceTree) {
			fmt.Fprintf(os.Stderr, "  %s\n", a.Caption)
		}
		return
	}
	fmt.Printf(i18n.T("install_removed")+"\n", removed)
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

// sourceTreeActions is the single definition of the nine custom actions,
// shared by the JXA installer and the manual-fallback printout.
func sourceTreeActions(st config.SourceTree) []staiAction {
	return []staiAction{
		{st.ReviewActionCaption, "review -no-color $REPO", st.ReviewShortcutKeyCode, st.ReviewShortcutModifiers, st.ReviewShortcutDisplay, true},
		{st.ActionCaption, "gen $REPO", st.ShortcutKeyCode, st.ShortcutModifiers, st.ShortcutDisplay, false},
		{st.PRActionCaption, "pr $REPO", st.PRShortcutKeyCode, st.PRShortcutModifiers, st.PRShortcutDisplay, false},
		{st.PRTitleActionCaption, "pr-title $REPO", 0, 0, "", false},
		{st.ReviewBranchActionCaption, "review-branch -no-color $REPO", st.ReviewBranchShortcutKeyCode, st.ReviewBranchShortcutModifiers, st.ReviewBranchShortcutDisplay, true},
		{st.StashMsgActionCaption, "stash-msg $REPO", st.StashMsgShortcutKeyCode, st.StashMsgShortcutModifiers, st.StashMsgShortcutDisplay, false},
		{st.ExplainActionCaption, "explain -file=$FILE -repo=$REPO", 0, 0, "", true},
		{st.SplitActionCaption, "split -repo=$REPO", 0, 0, "", false},
		{st.ChangelogActionCaption, "changelog -repo=$REPO", 0, 0, "", false},
	}
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
	data, err := json.Marshal(sourceTreeActions(st))
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
	captions, err := json.Marshal([]string{st.ActionCaption, st.ReviewActionCaption, st.PRActionCaption, st.PRTitleActionCaption, st.ReviewBranchActionCaption, st.StashMsgActionCaption, st.ExplainActionCaption, st.SplitActionCaption, st.ChangelogActionCaption})
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
// see nothing before pasting. An empty configured subtitle falls back to the
// language-dependent default.
func notifyCopied(n config.Notify, msg string) {
	subtitle := n.Subtitle
	if subtitle == "" {
		subtitle = i18n.T("notify_subtitle")
	}
	notify(n.Title, subtitle, msg)
}

// resolveRealPath returns the absolute, symlink-free form of p. Symlink
// resolution falls back to the lexical absolute path when a component cannot
// be resolved (e.g. the file does not exist yet).
func resolveRealPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
}

// validateFileInRepo ensures filePath is inside repoDir, preventing a custom
// action from reading arbitrary files via a crafted $FILE. Both paths are
// resolved to their real (symlink-free) locations first, so a symlink inside
// the repo cannot point outside it; ".." traversal is rejected via
// filepath.Rel.
func validateFileInRepo(filePath, repoDir string) error {
	absFile, err := resolveRealPath(filePath)
	if err != nil {
		return fmt.Errorf("resolving file path: %w", err)
	}
	absRepo, err := resolveRealPath(repoDir)
	if err != nil {
		return fmt.Errorf("resolving repo path: %w", err)
	}
	rel, err := filepath.Rel(absRepo, absFile)
	if err != nil {
		return fmt.Errorf("checking file location: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("file %s is outside repository %s", filePath, repoDir)
	}
	return nil
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

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}
	reviewBaseURL, _, reviewModel, _ := reviewProvider(cfg)
	logf("review start args=%q model=%s base_url=%s", args, reviewModel, reviewBaseURL)

	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("staged_empty"))
		os.Exit(2)
	}

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff)
	if err != nil {
		logf("review error: %v", err)
		fatal(err)
	}
	logf("review done findings=%d high=%v", len(findings), ai.HasHigh(findings))

	report := renderFindings(findings, !*noColorFlag, true)
	fmt.Print(report)

	// SourceTree discards stdout, so the report also goes to a
	// notification: the full text when it fits, otherwise a summary plus
	// the report-file path. Notifications must stay plain-text (no ANSI) and
	// concise (no fixes).
	plainReport := renderFindings(findings, false, false)
	notifBody := plainReport
	subtitle := i18n.T("review_subtitle_done")
	if len(findings) > cfg.Review.NotifyMaxFindings {
		if path, werr := writeReportFile(cfg, repoDir, findings); werr == nil {
			notifBody = fmt.Sprintf(i18n.T("review_report_notice"), path, severitySummary(findings))
			subtitle = i18n.T("review_subtitle_file")
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
		SuggestFixes:  cfg.Review.SuggestFixes,
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
// terminal and SourceTree's full-output window). includeFixes controls whether
// SuggestedFix lines are printed (yes for stdout/report, no for notifications).
func renderFindings(findings []ai.Finding, color, includeFixes bool) string {
	if len(findings) == 0 {
		return i18n.T("review_ok") + "\n"
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
		if includeFixes && f.SuggestedFix != "" {
			fmt.Fprintf(&b, "  %s%s\n", i18n.T("review_fix_label"), f.SuggestedFix)
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
	return fmt.Sprintf(i18n.T("review_summary"), high, medium, low, other)
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
	content := fmt.Sprintf(i18n.T("review_report_title")+"%s\n%s\n",
		time.Now().Format("2006-01-02 15:04:05"), renderFindings(findings, false, true))
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
	fmt.Printf(i18n.T("mergetool_not_impl"), cmd, plan)
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
