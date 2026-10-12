// Command stai is the AI companion for SourceTree git workflows.
//
// It is not a SourceTree plugin (SourceTree has no plugin API); it hooks into
// git itself — hooks, mergetool wrappers, and SourceTree "custom actions" —
// so everything also works with any other git client.
package main

import (
	"fmt"
	"os"
	"strings"

	"stai/internal/ai"
	"stai/internal/color"
	"stai/internal/config"
	"stai/internal/i18n"
)

const version = "v0.1.0"

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
	if insecure, perm := config.FileInsecure(); insecure {
		fmt.Fprintf(os.Stderr, i18n.T("config_perms_insecure")+"\n", perm, config.GlobalPath())
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
	case "review-loop-gen":
		cmdReviewLoopGen(args)
	case "mcp":
		cmdMCP(args)
	case "mergetool":
		cmdMergetool(args)
	case "install":
		cmdInstall(args)
	case "uninstall":
		cmdUninstall(args)
	case "config":
		cmdConfig(args)
	case "doctor":
		cmdDoctor(args)
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, i18n.T("unknown_command")+"\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	header := i18n.T("usage_header")
	fmt.Fprintln(os.Stderr, color.Cyan(color.Bold(header)))

	groups := []struct {
		title string
		cmds  []string
	}{
		{i18n.T("usage_group_generate"), []string{"gen", "pr", "pr-title", "stash-msg", "split", "changelog"}},
		{i18n.T("usage_group_review"), []string{"review", "review-branch", "review-loop-gen"}},
		{i18n.T("usage_group_explain"), []string{"explain"}},
		{i18n.T("usage_group_setup"), []string{"install", "uninstall", "config", "doctor", "mcp", "version"}},
		{i18n.T("usage_group_system"), []string{"hook", "mergetool"}},
	}

	maxLen := 0
	for _, g := range groups {
		for _, c := range g.cmds {
			if len(c) > maxLen {
				maxLen = len(c)
			}
		}
	}

	for _, g := range groups {
		fmt.Fprintf(os.Stderr, "%s\n", color.Cyan(g.title))
		for _, c := range g.cmds {
			key := "usage_cmd_" + strings.ReplaceAll(c, "-", "_")
			fmt.Fprintf(os.Stderr, "  %s  %s\n",
				color.Green(fmt.Sprintf("%-*s", maxLen, c)),
				color.Gray(i18n.T(key)))
		}
	}
	fmt.Fprint(os.Stderr, "\n"+i18n.T("usage_footer"))
}