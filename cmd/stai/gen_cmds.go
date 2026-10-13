package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("staged_empty"))
		os.Exit(1)
	}

	ctx, cancel := commandContext(context.Background(), time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := generate(ctx, cfg, diff)
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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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
	// Resolve to absolute path early for security
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fatal(fmt.Errorf("resolve repo dir: %w", err))
	}
	repoDir = absRepoDir

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