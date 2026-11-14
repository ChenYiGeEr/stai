package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"stai/internal/ai"
	"stai/internal/i18n"
	"stai/internal/mergetool"
)

// cmdMergetool resolves one text file passed by git mergetool.
// A real terminal runs the session here. Without a TTY, macOS opens
// Terminal.app on a .command file and this process only relays that
// session's exit code.
func cmdMergetool(args []string) {
	os.Exit(runMergetool(args, os.Stdin, os.Stdout, os.Stderr))
}

func runMergetool(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mergetool", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "common ancestor ($BASE)")
	local := fs.String("local", "", "ours ($LOCAL)")
	remote := fs.String("remote", "", "theirs ($REMOTE)")
	merged := fs.String("merged", "", "file being merged ($MERGED)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: stai mergetool --base BASE --local LOCAL --remote REMOTE --merged MERGED\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *merged == "" {
		fmt.Fprintln(stderr, i18n.T("mergetool_missing_merged"))
		fs.Usage()
		return 2
	}

	// SourceTree and git mergetool start this process with no TTY. Do not
	// read keys or write $MERGED here. A macOS Terminal session does that
	// and we relay its exit code. Any other system stops without writing.
	inTerminal := os.Getenv("STAI_MERGETOOL_TERMINAL") == "1"
	if inTerminal && !stdinIsTerminal(in) {
		fmt.Fprintln(stderr, i18n.T("mergetool_terminal_not_tty"))
		return 1
	}
	if !inTerminal && !stdinIsTerminal(in) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		code, err := launchMergetoolTerminal(ctx, *base, *local, *remote, *merged)
		if err != nil {
			fmt.Fprintf(stderr, i18n.T("mergetool_no_terminal")+"\n", err)
			return 1
		}
		return code
	}

	cfg := loadConfig()
	logf("mergetool start merged=%s model=%s", *merged, cfg.Provider.Model)

	client := ai.NewClient(cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = cfg.Provider.Temperature
	client.Logf = logf
	timeout := time.Duration(cfg.Provider.TimeoutSec) * time.Second

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return mergetool.Run(ctx, mergetool.Options{
		Base:   *base,
		Local:  *local,
		Remote: *remote,
		Merged: *merged,
		In:     in,
		Out:    stdout,
		Advise: func(ctx context.Context, h mergetool.Hunk) (mergetool.Advice, error) {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			explanation, choice, resolution, err := client.SuggestMergeHunk(cctx, string(h.Base), string(h.Ours), string(h.Theirs))
			if err != nil {
				return mergetool.Advice{}, err
			}
			return mergetool.Advice{Explanation: explanation, Choice: choice, Resolution: resolution}, nil
		},
	})
}
