package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"stai/internal/ai"
	"stai/internal/color"
	"stai/internal/config"
	"stai/internal/i18n"
)

// errWizardCancelled is returned when the user aborts the install/config
// wizard on the final summary screen.
var errWizardCancelled = errors.New("wizard cancelled")

// inputReader wraps stdin so prompts can be tested or redirected consistently.
var inputReader = bufio.NewReader(os.Stdin)

// separator prints a horizontal rule in the active color theme.
func separator() {
	fmt.Println(color.Gray(strings.Repeat("─", 48)))
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visibleLen returns the number of terminal columns occupied by s after
// stripping ANSI escape sequences. CJK, Hiragana, Katakana, Hangul and
// full-width forms are counted as 2 columns.
func visibleLen(s string) int {
	n := 0
	for _, r := range ansiRe.ReplaceAllString(s, "") {
		if isWideRune(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// isWideRune reports whether r is typically rendered as two terminal columns.
// It covers CJK ideographs, Hiragana, Katakana, Hangul, full-width forms and
// common CJK punctuation/enclosed symbols.
func isWideRune(r rune) bool {
	switch {
	case inRange(r, 0x1100, 0x11FF): // Hangul Jamo
		return true
	case inRange(r, 0x2E80, 0x9FFF): // CJK radicals, Unified Ideographs, compat
		return true
	case inRange(r, 0xA000, 0xA4CF): // Yi
		return true
	case inRange(r, 0xA960, 0xA97F): // Hangul Jamo Extended-A
		return true
	case inRange(r, 0xAC00, 0xD7AF): // Hangul Syllables
		return true
	case inRange(r, 0xD7B0, 0xD7FF): // Hangul Jamo Extended-B
		return true
	case inRange(r, 0xF900, 0xFAFF): // CJK Compatibility Ideographs
		return true
	case inRange(r, 0xFE10, 0xFE1F): // Vertical forms
		return true
	case inRange(r, 0xFE30, 0xFE6F): // CJK compatibility forms, small form variants
		return true
	case inRange(r, 0xFF00, 0xFFEF): // Halfwidth/Fullwidth forms
		return true
	case inRange(r, 0x2600, 0x27BF): // Miscellaneous symbols, Dingbats (emoji)
		return true
	case inRange(r, 0x1F300, 0x1FAFF): // Emoji & pictographs
		return true
	case inRange(r, 0x1F200, 0x1F2FF): // Enclosed ideographic supplement
		return true
	case inRange(r, 0x2F800, 0x2FA1F): // CJK Compatibility Ideographs Supplement
		return true
	}
	return false
}

func inRange(r, lo, hi rune) bool { return r >= lo && r <= hi }

// wizardHeader prints the framed title at the start of the wizard.
func wizardHeader() {
	title := i18n.T("wizard_header_title")
	sub := i18n.T("wizard_header_subtitle")
	inner := fmt.Sprintf("  %s  ·  %s  ", color.Bold(title), sub)
	border := strings.Repeat("─", visibleLen(inner))
	fmt.Println(color.Cyan("╭" + border + "╮"))
	fmt.Println(color.Cyan("│") + color.Cyan(inner) + color.Cyan("│"))
	fmt.Println(color.Cyan("╰" + border + "╯"))
}

// stepTitle prints a progress marker like "[1/5] Language".
func stepTitle(current, total int, label string) {
	fmt.Printf("\n%s %s\n", color.Cyan(fmt.Sprintf("[%d/%d]", current, total)), color.Bold(label))
	separator()
}

// readLine prints prompt and reads one line from stdin. The returned string
// has surrounding whitespace removed; an error reading is treated as EOF.
func readLine(prompt string) string {
	fmt.Print(prompt)
	line, err := inputReader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

// promptString asks for a string, returning defaultValue when the user just
// presses Enter. The default is shown in brackets.
func promptString(label, defaultValue string) string {
	var prompt string
	if defaultValue != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, defaultValue)
	} else {
		prompt = fmt.Sprintf("%s: ", label)
	}
	v := readLine(prompt)
	if v == "" {
		return defaultValue
	}
	return v
}

// readPassword asks for a secret without echoing it. If the terminal cannot
// be switched to no-echo mode, it falls back to a visible prompt and warns
// the user. Echo is always restored, even if the process receives SIGINT,
// SIGTERM or SIGHUP.
func readPassword(label string) string {
	// Register signal handlers before disabling echo so there is no window
	// where a terminating signal could exit while the terminal is silent.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	if disableEcho() == nil {
		done := make(chan struct{})
		defer func() {
			// Restore echo while the signal handler is still active, so a
			// signal during teardown cannot leave the terminal silent.
			if err := enableEcho(); err != nil {
				logf("enableEcho failed: %v", err)
			}
			// Close done first: the goroutine re-checks done after receiving
			// a signal, so once done is closed it can never call os.Exit,
			// even if a signal is still buffered in sigCh.
			close(done)
			signal.Stop(sigCh)
		}()
		go func() {
			select {
			case <-done:
				return
			case sig := <-sigCh:
				// Re-check done: the main flow may have finished and closed
				// done while this signal was in flight. Only exit while the
				// password read is still active.
				select {
				case <-done:
					return
				default:
				}
				if err := enableEcho(); err != nil {
					logf("enableEcho on signal failed: %v", err)
				}
				os.Exit(128 + int(sig.(syscall.Signal)))
			}
		}()
		fmt.Print(label + ": ")
		line, err := inputReader.ReadString('\n')
		fmt.Println()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(line)
	}

	signal.Stop(sigCh)
	fmt.Println()
	fmt.Println(color.Yellow(i18n.T("wizard_api_key_fallback")))
	return promptString(label, "")
}

// choose shows a numbered list and returns the selected index. Empty input
// selects defaultIdx. Invalid input is rejected and the prompt repeats.
func choose(label string, options []string, defaultIdx int) int {
	fmt.Println(label)
	for i, o := range options {
		fmt.Printf("  %d) %s\n", i+1, o)
	}
	for {
		v := readLine(fmt.Sprintf("? [%d]: ", defaultIdx+1))
		if v == "" {
			return defaultIdx
		}
		n, err := strconv.Atoi(v)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Println(color.Yellow(i18n.T("wizard_invalid_choice")))
	}
}

// multiSelect shows a numbered list and returns the selected indices.
// Empty input or "all" returns every item; "0" returns none. Items may be
// selected by comma-separated numbers. Invalid input causes the prompt to
// repeat, matching choose/confirm behavior.
func multiSelect(label string, items []string) []int {
	fmt.Println(label)
	for i, it := range items {
		fmt.Printf("  %d) %s\n", i+1, it)
	}
	fmt.Printf("  %d) %s\n", 0, i18n.T("wizard_actions_none"))

	all := make([]int, len(items))
	for i := range items {
		all[i] = i
	}

	for {
		v := strings.ToLower(strings.TrimSpace(readLine("? [all]: ")))
		if v == "" || v == "all" || v == "a" {
			return all
		}
		if v == "0" {
			return nil
		}

		var out []int
		seen := make(map[int]bool)
		valid := true
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > len(items) {
				fmt.Printf("%s: %s\n", color.Yellow(i18n.T("wizard_invalid_choice")), part)
				valid = false
				break
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n-1)
		}
		if valid && len(out) > 0 {
			return out
		}
	}
}

// confirm asks a yes/no question. defaultYes controls the prompt letter and
// what an empty answer means.
func confirm(prompt string, defaultYes bool) bool {
	var suffix string
	if defaultYes {
		suffix = " [Y/n]: "
	} else {
		suffix = " [y/N]: "
	}
	for {
		v := strings.ToLower(readLine(prompt + suffix))
		if v == "" {
			return defaultYes
		}
		if v == "y" || v == "yes" {
			return true
		}
		if v == "n" || v == "no" {
			return false
		}
		fmt.Println(color.Yellow(i18n.T("wizard_invalid_choice")))
	}
}

// envDefault returns envValue if set, otherwise fallback.
func envDefault(envValue, fallback string) string {
	if envValue != "" {
		return envValue
	}
	return fallback
}

// maskKey returns a display-safe form of an API key.
func maskKey(key string) string {
	if key == "" {
		return "(empty)"
	}
	r := []rune(key)
	if len(r) <= 4 {
		return "***"
	}
	return "***" + string(r[len(r)-4:])
}

// runInstallWizard interactively collects the minimum config and returns the
// resulting Config plus the SourceTree actions the user wants to register.
// When skipActions is true, the action-selection step is omitted and the
// returned action slice is nil.
// It must only be called when stdin is a terminal.
func runInstallWizard(skipActions bool) (config.Config, []staiAction, error) {
	wizardHeader()
	fmt.Println(color.Green(i18n.T("wizard_welcome")))

	cfg := config.Default()

	// Step 1: language. The prompt is bilingual so it works before i18n is
	// switched to the chosen language.
	stepTitle(1, 5, i18n.T("wizard_step_lang"))
	langIdx := choose(i18n.T("wizard_choose_lang"), []string{
		i18n.T("wizard_lang_zh"),
		i18n.T("wizard_lang_en"),
	}, 0)
	if langIdx == 1 {
		cfg.Global.Lang = "en"
	} else {
		cfg.Global.Lang = "zh-CN"
	}
	i18n.SetLang(cfg.Global.Lang)
	ai.SetLang(cfg.Global.Lang)

	// Step 2: provider basics, with env defaults.
	stepTitle(2, 5, i18n.T("wizard_step_provider"))
	cfg.Provider.BaseURL = promptString(i18n.T("wizard_base_url"), envDefault(os.Getenv("STAI_BASE_URL"), cfg.Provider.BaseURL))
	cfg.Provider.APIKey = readPassword(i18n.T("wizard_api_key"))
	if cfg.Provider.APIKey == "" {
		cfg.Provider.APIKey = os.Getenv("STAI_API_KEY")
	}

	// Step 3: model selection via /models, with manual fallback.
	stepTitle(3, 5, i18n.T("wizard_step_model"))
	cfg.Provider.Model = chooseModel(cfg, envDefault(os.Getenv("STAI_MODEL"), cfg.Provider.Model))

	// Step 4: SourceTree actions selection (skipped when -no-sourcetree or
	// when reconfiguring via stai config).
	var selectedActions []staiAction
	if !skipActions {
		stepTitle(4, 5, i18n.T("wizard_step_actions"))
		actions := sourceTreeActions(cfg.SourceTree)
		names := make([]string, len(actions))
		for i, a := range actions {
			names[i] = a.Caption
		}
		selected := multiSelect(i18n.T("wizard_actions_prompt"), names)

		if len(selected) == 0 {
			fmt.Println(color.Yellow(i18n.T("wizard_actions_none")))
		} else if len(selected) == len(actions) {
			fmt.Println(color.Green(i18n.T("wizard_actions_all")))
		} else {
			fmt.Printf(color.Green(i18n.T("wizard_actions_done"))+"\n", len(selected))
		}
		for _, idx := range selected {
			selectedActions = append(selectedActions, actions[idx])
		}
	}

	// Step 5: summary and confirm.
	stepTitle(5, 5, i18n.T("wizard_step_summary"))
	printSummary(cfg, selectedActions)
	if !confirm(i18n.T("wizard_summary_prompt"), true) {
		fmt.Println(color.Yellow(i18n.T("wizard_cancelled")))
		return config.Config{}, nil, errWizardCancelled
	}

	// Step 6: write the config file.
	if err := config.WriteGlobal(cfg, cfg.Global.Lang); err != nil {
		fatal(fmt.Errorf("writing config: %w", err))
	}
	fmt.Printf(color.Green(i18n.T("wizard_config_written"))+"\n", config.GlobalPath())

	return cfg, selectedActions, nil
}

// printSummary shows the choices before writing the config.
func printSummary(cfg config.Config, actions []staiAction) {
	var actionText string
	switch {
	case len(actions) == 0:
		actionText = i18n.T("wizard_actions_none")
	case len(actions) == len(sourceTreeActions(cfg.SourceTree)):
		actionText = i18n.T("wizard_actions_all")
	default:
		actionText = fmt.Sprintf(i18n.T("wizard_actions_done"), len(actions))
	}

	fmt.Printf("  %s: %s\n", color.Bold(i18n.T("wizard_summary_lang")), cfg.Global.Lang)
	fmt.Printf("  %s: %s\n", color.Bold(i18n.T("wizard_summary_base_url")), cfg.Provider.BaseURL)
	fmt.Printf("  %s: %s\n", color.Bold(i18n.T("wizard_summary_api_key")), maskKey(cfg.Provider.APIKey))
	fmt.Printf("  %s: %s\n", color.Bold(i18n.T("wizard_summary_model")), cfg.Provider.Model)
	fmt.Printf("  %s: %s\n", color.Bold(i18n.T("wizard_summary_actions")), actionText)
	fmt.Println()
}

// chooseModel tries to list models from the provider and let the user pick;
// on any failure it falls back to a plain text prompt.
func chooseModel(cfg config.Config, defaultModel string) string {
	fmt.Println(i18n.T("wizard_model_loading"))
	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model, 15*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil || len(models) == 0 {
		if err != nil {
			logf("wizard model list failed: %v", err)
		}
		if len(models) == 0 {
			fmt.Println(color.Yellow(i18n.T("wizard_model_list_empty")))
		}
		return promptString(i18n.T("wizard_model_manual"), defaultModel)
	}

	idx := choose(i18n.T("wizard_model_prompt"), models, 0)
	return models[idx]
}
