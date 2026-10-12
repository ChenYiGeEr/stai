package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

const staiHookMarker = "installed by stai"

// shellQuote returns a shell-safe single-quoted version of s.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

var errWizardCancelled = errors.New("wizard cancelled")

// cmdInstall writes the prepare-commit-msg, pre-commit, and pre-push hooks
// into the current repository and registers the SourceTree custom actions
// (gen, review, pr, review-branch, stash-msg). Idempotent: stai-owned hooks
// and actions are replaced, everything else is left alone.
func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	noSourceTree := fs.Bool("no-sourcetree", false, "skip registering the SourceTree custom actions")
	fs.Parse(args)

	var (
		cfg             config.Config
		selectedActions []staiAction
		wizardRan       bool
	)

	globalPath := config.GlobalPath()
	if globalPath != "" {
		_, err := os.Stat(globalPath)
		if os.IsNotExist(err) && isTerminal(os.Stdin.Fd()) {
			var werr error
			cfg, selectedActions, werr = runInstallWizard(*noSourceTree)
			if errors.Is(werr, errWizardCancelled) {
				os.Exit(0)
			}
			if werr != nil {
				fatal(werr)
			}
			wizardRan = true
		} else if err != nil {
			fatal(fmt.Errorf("checking config file: %w", err))
		} else {
			cfg = loadConfig()
		}
		// If config file doesn't exist and we're not in a terminal,
		// load defaults so SourceTree actions get proper captions.
		if !wizardRan && cfg.Provider.BaseURL == "" {
			cfg = loadConfig()
		}
	} else {
		cfg = loadConfig()
	}

	exe, err := os.Executable()
	fatal(err)
	exe, err = filepathEvalSymlinks(exe)
	fatal(err)

	quotedExe := shellQuote(exe)

	hooks := []struct{ name, body string }{
		{
			"prepare-commit-msg",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — fills in an AI-generated message when the commit
# message is left empty. Never blocks a commit.
%s hook prepare-commit-msg "$@" || exit 0
`, quotedExe),
		},
		{
			"pre-commit",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — strict-mode pre-commit review (see review.strict).
# Blocks only when stai itself reports high-severity findings (exit 1);
# passes on every other outcome.
%s hook pre-commit
status=$?
[ "$status" -eq 1 ] && exit 1
exit 0
`, quotedExe),
		},
		{
			"pre-push",
			fmt.Sprintf(`#!/bin/sh
# installed by stai — strict-mode pre-push review (see pre_push.strict).
# Blocks only when stai itself reports high-severity findings (exit 1);
# passes on every other outcome.
%s hook pre-push
status=$?
[ "$status" -eq 1 ] && exit 1
exit 0
`, quotedExe),
		},
	}
	for _, h := range hooks {
		path, err := git.HookPath(h.name)
		fatal(err)
		existing, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				// Other error (e.g., permission denied): don't overwrite user's hook
				fmt.Fprintf(os.Stderr, "stai: reading hook %s: %v\n", path, err)
				continue
			}
			// File doesn't exist - proceed to write
		} else if !strings.Contains(string(existing), staiHookMarker) {
			// File exists but not owned by stai - leave it alone
			fmt.Printf(i18n.T("install_hook_left_alone")+"\n", path)
			continue
		}
		if err := os.WriteFile(path, []byte(h.body), 0o755); err != nil {
			fatal(fmt.Errorf("writing hook: %w", err))
		}
		fmt.Printf(i18n.T("install_hook_installed")+"\n", path)
	}

	if *noSourceTree {
		return
	}

	actions := selectedActions
	if !wizardRan {
		actions = sourceTreeActions(cfg.SourceTree)
	}
	if len(actions) == 0 {
		return
	}
	if err := installSourceTreeActions(exe, actions); err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("install_actions_failed"), err)
		fmt.Fprintln(os.Stderr, i18n.T("install_add_manually"))
		for _, a := range actions {
			fmt.Fprintf(os.Stderr, i18n.T("install_caption")+"\n", a.Caption)
			fmt.Fprintf(os.Stderr, i18n.T("install_script")+"\n", exe)
			fmt.Fprintf(os.Stderr, i18n.T("install_params")+"\n", a.Params)
		}
		return
	}
	captions := make([]string, 0, len(actions))
	for _, a := range actions {
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

	removeConfig := false
	if globalPath := config.GlobalPath(); globalPath != "" && isTerminal(os.Stdin.Fd()) {
		if _, err := os.Stat(globalPath); err == nil {
			removeConfig = confirm(fmt.Sprintf(i18n.T("uninstall_config_prompt"), globalPath), false)
		}
	}

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
		if !strings.Contains(string(data), staiHookMarker) {
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
		// Still run log/config cleanup and final message
	} else {
		removed, err := uninstallSourceTreeActions(exe, cfg.SourceTree)
		if err != nil {
			fmt.Fprintf(os.Stderr, i18n.T("install_remove_failed"), err)
			fmt.Fprintln(os.Stderr, i18n.T("install_remove_manually"))
			for _, a := range sourceTreeActions(cfg.SourceTree) {
				fmt.Fprintf(os.Stderr, "  %s\n", a.Caption)
			}
			// Still run log/config cleanup and final message
		} else {
			fmt.Printf(i18n.T("install_removed")+"\n", removed)
		}
	}

	// Delete the log file (best-effort: never block uninstall because of it).
	if cfg.Log.Path != "" {
		if err := os.Remove(config.ExpandHome(cfg.Log.Path)); err != nil && !os.IsNotExist(err) {
			logf("uninstall log removal failed: %v", err)
			fmt.Fprintf(os.Stderr, i18n.T("uninstall_log_failed")+"\n", err)
		}
	}

	if removeConfig {
		globalPath := config.GlobalPath()
		if err := os.Remove(globalPath); err == nil {
			fmt.Printf(i18n.T("uninstall_config_removed")+"\n", globalPath)
		} else if !os.IsNotExist(err) {
			logf("uninstall config removal failed: %v", err)
			fmt.Fprintf(os.Stderr, i18n.T("uninstall_config_failed")+"\n", err)
		}
	}

	fmt.Println(i18n.T("uninstall_done"))
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
func installSourceTreeActions(exe string, actions []staiAction) error {
	data, err := json.Marshal(actions)
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
		// Drop all entries owned by this exe (target matches), regardless of caption.
		// This ensures that if the user changed captions or moved the binary,
		// old stai entries don't become orphaned.
		if (t && t.isEqualToString(exe)) continue;
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