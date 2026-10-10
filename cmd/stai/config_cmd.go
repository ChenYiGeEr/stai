package main

import (
	"flag"
	"fmt"
	"os"

	"stai/internal/color"
	"stai/internal/config"
	"stai/internal/i18n"
)

// cmdConfig implements "stai config". With -path it prints the global config
// path and exits. Otherwise it re-runs the install wizard even if a config
// already exists, backing up the old file first.
func cmdConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	pathFlag := fs.Bool("path", false, "print the global config path and exit")
	fs.Parse(args)

	globalPath := config.GlobalPath()
	if globalPath == "" {
		fatal(fmt.Errorf("cannot determine global config path"))
	}

	if *pathFlag {
		fmt.Println(globalPath)
		return
	}

	if !isTerminal(os.Stdin.Fd()) {
		fmt.Fprintln(os.Stderr, "stai config: a TTY is required for the interactive wizard")
		os.Exit(2)
	}

	// Back up the existing config only when there is one to back up.
	if _, err := os.Stat(globalPath); err == nil {
		if berr := config.BackupGlobal(); berr != nil {
			fatal(fmt.Errorf("backing up existing config: %w", berr))
		}
		fmt.Printf(i18n.T("config_backup_done")+"\n", globalPath+".bak")
	} else if !os.IsNotExist(err) {
		fatal(fmt.Errorf("checking config file: %w", err))
	}

	// Reconfigure only the config file; skip the SourceTree action selection.
	cfg, _, err := runInstallWizard(true)
	if err == errWizardCancelled {
		os.Exit(0)
	}
	fatal(err)
	_ = cfg
	fmt.Println(color.Green(i18n.T("wizard_reconfigure_done")))
}
