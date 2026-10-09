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
	logf("gen start args=%q", args)

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
	if err := copyToClipboard(msg); err != nil {
		logf("gen clipboard copy failed: %v", err)
		fmt.Fprintf(os.Stderr, "stai: clipboard copy failed: %v\n", err)
		return
	}
	logf("gen copied")
	notifyCopied(cfg.Notify, msg)
	fmt.Fprintln(os.Stderr, "copied to clipboard")
	fmt.Println(msg)
}

// generate asks the configured model for a commit message for diff. Both
// gen and the hook use it so the provider and rules are wired in one place.
func generate(ctx context.Context, cfg config.Config, diff []byte) (string, error) {
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	return client.GenerateCommit(ctx, cfg.Commit, diff)
}

// dialogEdit shows the generated message in an editable macOS dialog. It
// returns the edited text, or ok=false when the user cancels or the dialog
// fails (never block the workflow because of a UI hiccup).
func dialogEdit(msg string) (edited string, ok bool) {
	script := `on run argv
	return text returned of (display dialog "可编辑,确定后复制到剪贴板,再到 SourceTree 提交框粘贴" default answer (item 1 of argv) buttons {"取消", "确定"} default button "确定" cancel button "取消" with title "stai — 编辑提交信息")
end run`
	cmd := exec.Command("osascript", "-e", script, "--", msg)
	out, err := cmd.Output()
	if err != nil {
		return "", false // -128 = user cancelled
	}
	return strings.TrimRight(string(out), "\n"), true
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := generate(ctx, cfg, diff)
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

	cfg, err := config.Load()
	fatal(err)
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
	if err := installSourceTreeAction(exe, cfg.SourceTree); err != nil {
		fmt.Fprintf(os.Stderr, `SourceTree custom action not registered: %v

Add it manually: SourceTree → Settings → Custom Actions → Add:
  Menu caption:  %s
  Script to run: %s
  Parameters:    gen $REPO
Then run "stai gen" in the repo (it copies the message to the clipboard).
`, err, cfg.SourceTree.ActionCaption, exe)
		return
	}
	fmt.Printf("SourceTree custom action registered: %s (restart SourceTree)\n", cfg.SourceTree.ActionCaption)
}

// installSourceTreeAction registers the custom action in SourceTree's real
// action storage: ~/Library/Application Support/SourceTree/actions.plist,
// an NSKeyedArchiver plist of mutable dictionaries. The schema below was
// captured from a Sourcetree 4.2.19-generated entry (verified at runtime:
// adding an action in the UI rewrites exactly this file). The defaults
// "customActions" key is a legacy migration path and no longer feeds the
// UI, which is why the original defaults-based install never showed up.
// Read-modify-write runs in one JXA script so a crash midway cannot corrupt
// the file; entries owned by other tools are preserved.
func installSourceTreeAction(exe string, st config.SourceTree) error {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", actionPlistJXA,
		"--", exe, st.ActionCaption, "gen $REPO",
		strconv.Itoa(st.ShortcutKeyCode), strconv.Itoa(st.ShortcutModifiers), st.ShortcutDisplay)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("updating actions.plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// actionPlistJXA deduplicates and appends the stai entry in actions.plist.
// argv: exe path, menu caption, parameters ($REPO is expanded by SourceTree
// to the repository path at run time), shortcut key code, shortcut
// modifiers, shortcut display text. The action runs "gen" directly — no
// dialog — because review and edits happen in the commit box itself.
const actionPlistJXA = `function run(argv) {
	ObjC.import('Foundation');
	var exe = argv[0], caption = argv[1], params = argv[2];
	var keyCode = parseInt(argv[3], 10), modifiers = parseInt(argv[4], 10), display = argv[5];
	var path = $.NSHomeDirectory().stringByAppendingPathComponent('Library/Application Support/SourceTree/actions.plist');
	var list = $.NSMutableArray.alloc.init;
	var fm = $.NSFileManager.defaultManager;
	if (fm.fileExistsAtPath(path)) {
		var data = $.NSData.dataWithContentsOfFile(path);
		if (data && data.length > 0) {
			var obj = $.NSKeyedUnarchiver.unarchiveObjectWithData(data);
			if (obj && obj.isKindOfClass($.NSArray.class)) {
				list = $.NSMutableArray.arrayWithArray(obj);
			}
		}
	}
	var kept = $.NSMutableArray.alloc.init;
	for (var i = 0; i < list.count; i++) {
		var d = list.objectAtIndex(i);
		var t = d.objectForKey('target');
		var n = d.objectForKey('name');
		if (t && t.isEqualToString(exe)) continue;
		if (n && n.isEqualToString(caption)) continue;
		kept.addObject(d);
	}
	var e = $.NSMutableDictionary.alloc.init;
	e.setObjectForKey(caption, 'name');
	e.setObjectForKey(exe, 'target');
	e.setObjectForKey(params, 'params');
	e.setObjectForKey($.NSNumber.numberWithBool(false), 'fileAction');
	e.setObjectForKey($.NSNumber.numberWithBool(false), 'logAction');
	e.setObjectForKey($.NSNumber.numberWithBool(false), 'separateWindow');
	e.setObjectForKey($.NSNumber.numberWithBool(false), 'showFullOutput');
	e.setObjectForKey($.NSNumber.numberWithInt(0), 'repoAction');
	e.setObjectForKey($.NSNumber.numberWithInt(keyCode), 'shortcutKeyCode');
	e.setObjectForKey($.NSNumber.numberWithInt(modifiers), 'shortcutKeyModifiers');
	e.setObjectForKey(display, 'shortcutKeyDisplay');
	kept.addObject(e);
	var out = $.NSKeyedArchiver.archivedDataWithRootObject(kept);
	if (!out.writeToFileAtomically(path, true)) throw Error('write failed: ' + path);
	return 'actions.plist now has ' + kept.count + ' entries';
}`

func copyToClipboard(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// notifyCopied surfaces the generated message in a macOS notification —
// SourceTree custom actions swallow stdout, so without it the user would
// see nothing before pasting. Best-effort: a notification failure must
// never break an otherwise successful copy.
func notifyCopied(n config.Notify, msg string) {
	script := `on run argv
	display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv)
end run`
	cmd := exec.Command("osascript", "-e", script, "--", msg, n.Title, n.Subtitle)
	_ = cmd.Run()
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

