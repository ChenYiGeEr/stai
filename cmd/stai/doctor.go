package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/color"
	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

// cmdDoctor implements "stai doctor": a quick self-check of the stai setup.
func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	fs.Parse(args)

	cfg := loadConfig()
	globalPath := config.GlobalPath()

	fmt.Println(color.Cyan(color.Bold(i18n.T("doctor_title"))))
	fmt.Println(strings.Repeat("─", 40))

	// Config file
	if globalPath != "" {
		if info, err := os.Stat(globalPath); err == nil {
			ok(i18n.T("doctor_config_exists"))
			perm := info.Mode().Perm()
			infof(fmt.Sprintf(i18n.T("doctor_config_perms"), perm))
			if perm&0o077 != 0 {
				warn(fmt.Sprintf(i18n.T("doctor_config_perms_insecure"), perm, globalPath))
			}
		} else {
			warn(i18n.T("doctor_config_missing"))
		}
	}

	// Env overrides
	var envs []string
	for _, k := range []string{"STAI_LANG", "STAI_BASE_URL", "STAI_API_KEY", "STAI_MODEL"} {
		if os.Getenv(k) != "" {
			envs = append(envs, k)
		}
	}
	if len(envs) > 0 {
		infof(fmt.Sprintf(i18n.T("doctor_env_active"), strings.Join(envs, ", ")))
	}

	// Provider reachability
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	models, err := client.ListModels(ctx)
	cancel()
	if err == nil {
		ok(fmt.Sprintf(i18n.T("doctor_provider_ok"), len(models)))

		// Model validity: check if configured model is in the provider's model list
		if cfg.Provider.Model != "" {
			found := false
			for _, m := range models {
				if m == cfg.Provider.Model {
					found = true
					break
				}
			}
			if found {
				ok(fmt.Sprintf(i18n.T("doctor_model_ok"), cfg.Provider.Model))
			} else {
				warn(fmt.Sprintf(i18n.T("doctor_model_warn"), cfg.Provider.Model))
			}
		}
	} else {
		fail(fmt.Sprintf(i18n.T("doctor_provider_fail"), err))
		infof(i18n.T("doctor_model_skip"))
	}

	// Git repo
	if _, err := git.RepoRoot(); err == nil {
		ok(i18n.T("doctor_git_repo"))
	} else {
		warn(i18n.T("doctor_not_git_repo"))
	}

	// Hooks
	installed := 0
	for _, name := range []string{"prepare-commit-msg", "pre-commit", "pre-push"} {
		path, err := git.HookPath(name)
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode()&0o111 != 0 {
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "installed by stai") {
				installed++
			}
		}
	}
	if installed == 3 {
		ok(fmt.Sprintf(i18n.T("doctor_hooks_ok"), installed))
	} else {
		warn(fmt.Sprintf(i18n.T("doctor_hooks_missing"), installed))
	}

	// SourceTree actions
	exe, err := os.Executable()
	if err == nil {
		if resolved, ferr := filepathEvalSymlinks(exe); ferr == nil {
			exe = resolved
		}
	}
	// Also check common symlink location (~/.local/bin/stai) in case actions.plist
	// was registered with the symlink path instead of the resolved binary path.
	var symlinkPath string
	home, _ := os.UserHomeDir()
	if home != "" {
		symlinkPath = filepath.Join(home, ".local", "bin", "stai")
		if fi, err := os.Lstat(symlinkPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(symlinkPath)
			// Resolve relative target to absolute path for comparison
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(symlinkPath), target)
				target, _ = filepath.EvalSymlinks(target)
			}
			// Only use it if it points to our executable
			if target == exe {
				// symlinkPath is valid
			} else {
				symlinkPath = ""
			}
		} else {
			symlinkPath = ""
		}
	}
	if count, err := countSourceTreeActions(exe, symlinkPath); err == nil {
		ok(fmt.Sprintf(i18n.T("doctor_actions_count"), count))
	} else {
		warn(fmt.Sprintf(i18n.T("doctor_actions_error"), err))
	}

	// Log writable
	if cfg.Log.Path != "" {
		p := config.ExpandHome(cfg.Log.Path)
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			f.Close()
			ok(fmt.Sprintf(i18n.T("doctor_log_writable"), p))
		} else {
			fail(fmt.Sprintf(i18n.T("doctor_log_not_writable"), err))
		}
	}
}

func ok(msg string)    { fmt.Printf("  %s %s\n", color.Green(i18n.T("doctor_ok")), msg) }
func warn(msg string)  { fmt.Printf("  %s %s\n", color.Yellow(i18n.T("doctor_warn")), msg) }
func fail(msg string)  { fmt.Printf("  %s %s\n", color.Red(i18n.T("doctor_fail")), msg) }
func infof(msg string) { fmt.Printf("  %s %s\n", color.Gray("[i]"), msg) }

// countSourceTreeActions returns how many stai entries are present in
// SourceTree's actions.plist without modifying the file.
func countSourceTreeActions(exe string, extraPaths ...string) (int, error) {
	args := []string{"osascript", "-l", "JavaScript", "-e", countActionsPlistJXA, "--", exe}
	args = append(args, extraPaths...)
	cmd := utf8Cmd(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// countActionsPlistJXA counts entries whose target matches any of argv[0..n].
// argv[0] = resolved exe path, argv[1..] = additional paths to match (e.g., symlink paths).
const countActionsPlistJXA = `function run(argv) {
	ObjC.import('Foundation');
	var targets = [];
	for (var i = 0; i < argv.length; i++) {
		if (argv[i] && argv[i].length > 0) targets.push(argv[i]);
	}
	var path = $.NSHomeDirectory().stringByAppendingPathComponent('Library/Application Support/SourceTree/actions.plist');
	var fm = $.NSFileManager.defaultManager;
	if (!fm.fileExistsAtPath(path)) return '0';
	var data = $.NSData.dataWithContentsOfFile(path);
	if (!data || data.length == 0) return '0';
	var list = null;
	try {
		list = $.NSKeyedUnarchiver.unarchiveObjectWithData(data);
		if (!(list && list.isKindOfClass && list.isKindOfClass($.NSArray.class))) return '0';
	} catch (e) {
		return '0';
	}
	var count = 0;
	for (var i = 0; i < list.count; i++) {
		var d = list.objectAtIndex(i);
		var t = d.objectForKey('target');
		if (t) {
			for (var j = 0; j < targets.length; j++) {
				if (t.isEqualToString(targets[j])) { count++; break; }
			}
		}
	}
	return '' + count;
}`
