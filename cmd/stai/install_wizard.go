package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/i18n"
)

// isTerminal reports whether fd is an interactive terminal. It avoids a
// third-party dependency by using the standard-library syscall directly.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&termios)))
	return err == 0
}

// inputReader wraps stdin so prompts can be tested or redirected consistently.
var inputReader = bufio.NewReader(os.Stdin)

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
		fmt.Println(i18n.T("wizard_invalid_choice"))
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
				fmt.Printf("%s: %s\n", i18n.T("wizard_invalid_choice"), part)
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
		fmt.Println(i18n.T("wizard_invalid_choice"))
	}
}

// runInstallWizard interactively collects the minimum config and returns the
// resulting Config plus the SourceTree actions the user wants to register.
// When skipActions is true, the action-selection step is omitted and the
// returned action slice is nil.
// It must only be called when stdin is a terminal.
func runInstallWizard(skipActions bool) (config.Config, []staiAction) {
	fmt.Println(i18n.T("wizard_welcome"))

	cfg := config.Default()

	// Step 1: language. The prompt is bilingual so it works before i18n is
	// switched to the chosen language.
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

	// Step 2: provider basics.
	cfg.Provider.BaseURL = promptString(i18n.T("wizard_base_url"), cfg.Provider.BaseURL)
	cfg.Provider.APIKey = promptString(i18n.T("wizard_api_key"), "")

	// Step 3: model selection via /models, with manual fallback.
	cfg.Provider.Model = chooseModel(cfg)

	// Step 4: write the config file before registering actions so every later
	// step can load it normally.
	if err := config.WriteGlobal(cfg, cfg.Global.Lang); err != nil {
		fatal(fmt.Errorf("writing config: %w", err))
	}
	fmt.Printf(i18n.T("wizard_config_written")+"\n", config.GlobalPath())

	// Step 5: SourceTree actions selection (skipped when -no-sourcetree).
	if skipActions {
		return cfg, nil
	}
	actions := sourceTreeActions(cfg.SourceTree)
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = a.Caption
	}
	selected := multiSelect(i18n.T("wizard_actions_prompt"), names)

	if len(selected) == 0 {
		fmt.Println(i18n.T("wizard_actions_none"))
		return cfg, nil
	}
	if len(selected) == len(actions) {
		fmt.Println(i18n.T("wizard_actions_all"))
	} else {
		fmt.Printf(i18n.T("wizard_actions_done")+"\n", len(selected))
	}
	out := make([]staiAction, 0, len(selected))
	for _, idx := range selected {
		out = append(out, actions[idx])
	}
	return cfg, out
}

// chooseModel tries to list models from the provider and let the user pick;
// on any failure it falls back to a plain text prompt.
func chooseModel(cfg config.Config) string {
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
			fmt.Println(i18n.T("wizard_model_list_empty"))
		}
		return promptString(i18n.T("wizard_model_manual"), cfg.Provider.Model)
	}

	idx := choose(i18n.T("wizard_model_prompt"), models, 0)
	return models[idx]
}
