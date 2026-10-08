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
// With -edit, the message first shows in an editable macOS dialog; the
// edited text is what gets copied. An optional positional argument is the
// repository path (SourceTree passes $REPO).
func cmdGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	copyFlag := fs.Bool("copy", true, "copy the generated message to the clipboard")
	editFlag := fs.Bool("edit", false, "show the message in an editable dialog before copying")
	fs.Parse(args)

	cfg, err := config.Load()
	fatal(err)

	// SourceTree custom actions append $REPO (the repository path) to the
	// parameters; the plain CLI passes nothing and uses the current dir.
	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model)
	msg, err := client.GenerateCommit(context.Background(), ai.CommitParams{
		Style:    cfg.Commit.Style,
		Language: cfg.Commit.Language,
		Diff:     diff,
		MaxChars: maxDiffChars,
		Retries:  1,
	})
	fatal(err)

	if *editFlag {
		edited, ok := dialogEdit(msg)
		if !ok {
			return // user cancelled: copy nothing
		}
		msg = edited
	}
	if *copyFlag && copyToClipboard(msg) == nil {
		fmt.Fprintln(os.Stderr, "copied to clipboard")
	}
	fmt.Println(msg)
}

// dialogEdit shows the generated message in an editable macOS dialog. It
// returns the edited text, or ok=false when the user cancels or the dialog
// fails (never block the workflow because of a UI hiccup).
func dialogEdit(msg string) (edited string, ok bool) {
	script := `on run argv
	return text returned of (display dialog "可编辑,确定后复制到剪贴板" default answer (item 1 of argv) buttons {"取消", "确定"} default button "确定" cancel button "取消" with title "stai — 编辑提交信息")
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

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model)
	msg, err := client.GenerateCommit(ctx, ai.CommitParams{
		Style:    cfg.Commit.Style,
		Language: cfg.Commit.Language,
		Diff:     diff,
		MaxChars: maxDiffChars,
		Retries:  1,
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
  Parameters:    gen --edit $REPO
Then run "stai gen" in the repo (it copies the message for the commit box).
`, err, exe)
		return
	}
	fmt.Println("SourceTree custom action registered: AI 生成提交信息 (restart SourceTree)")
}

// actionCaption is the menu caption of the SourceTree custom action.
const actionCaption = "AI 生成提交信息"

// installSourceTreeAction registers the custom action in SourceTree's real
// action storage: ~/Library/Application Support/SourceTree/actions.plist,
// an NSKeyedArchiver plist of mutable dictionaries. The schema below was
// captured from a Sourcetree 4.2.19-generated entry (verified at runtime:
// adding an action in the UI rewrites exactly this file). The defaults
// "customActions" key is a legacy migration path and no longer feeds the
// UI, which is why the original defaults-based install never showed up.
// Read-modify-write runs in one JXA script so a crash midway cannot corrupt
// the file; entries owned by other tools are preserved.
func installSourceTreeAction(exe string) error {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", actionPlistJXA,
		"--", exe, actionCaption, "gen --edit $REPO")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("updating actions.plist: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// actionPlistJXA deduplicates and appends the stai entry in actions.plist.
// argv: exe path, menu caption, parameters ($REPO is expanded by SourceTree
// to the repository path at run time).
const actionPlistJXA = `function run(argv) {
	ObjC.import('Foundation');
	var exe = argv[0], caption = argv[1], params = argv[2];
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
	e.setObjectForKey($.NSNumber.numberWithInt(-1), 'shortcutKeyCode');
	e.setObjectForKey($.NSNumber.numberWithInt(0), 'shortcutKeyModifiers');
	e.setObjectForKey('', 'shortcutKeyDisplay');
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
