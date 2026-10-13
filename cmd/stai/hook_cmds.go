package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

// cmdHook is the entrypoint git hooks call. prepare-commit-msg fills in a
// generated message when the commit box is left empty (never blocks); the
// strict-mode pre-commit gate blocks only on high-severity findings when
// review.strict is enabled.
func cmdHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, i18n.T("usage_hook"))
		os.Exit(2)
	}
	switch fs.Arg(0) {
	case "prepare-commit-msg":
		hookPrepareCommitMsg(fs.Arg(1), fs.Arg(2))
	case "pre-commit":
		hookPreCommit()
	case "pre-push":
		hookPrePush()
	default:
		fmt.Fprintf(os.Stderr, i18n.T("unknown_hook")+"\n", fs.Arg(0))
		os.Exit(2)
	}
}

// hookPrepareCommitMsg fills in a generated message only when the user left
// the commit message empty. It must never block a commit, so every failure
// path exits 0.
func hookPrepareCommitMsg(msgFile, source string) {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	// Merge and squash commits carry their own messages; never touch those.
	if source == "merge" || source == "squash" || source == "commit" {
		return
	}
	if msgFile == "" {
		return
	}
	data, err := os.ReadFile(msgFile)
	if err != nil {
		return // unreadable file: leave it alone
	}
	// Check if there's any non-comment, non-empty content
	hasContent := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			hasContent = true
			break
		}
	}
	if hasContent {
		return // existing message: leave it alone
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	setLangFromConfig(cfg)
	logPath = cfg.Log.Path
	logf("hook prepare-commit-msg start (message empty)")

	diff, err := git.StagedDiff()
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook prepare-commit-msg: nothing staged, skipping")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	msg, err := generate(ctx, cfg, diff)
	if err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("hook_msg_gen_failed")+"\n", err)
		return
	}
	logf("hook prepare-commit-msg message=%q", msg)
	if err := os.WriteFile(msgFile, []byte(msg+"\n"), 0o644); err != nil {
		logf("hook prepare-commit-msg: %v", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
	}
}

// hookPreCommit implements the strict-mode pre-commit gate (M2). It only
// acts when review.strict is enabled: the staged diff is reviewed and the
// commit is blocked (exit 1) when a high-severity finding is present, with
// the report on stderr (git/SourceTree show it on the failed commit).
// Everything else passes: no review without strict, and AI or parse
// failures never block a commit.
func hookPreCommit() {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	setLangFromConfig(cfg)
	logPath = cfg.Log.Path
	logf("hook pre-commit start strict=%v", cfg.Review.Strict)
	if !cfg.Review.Strict {
		return // advisory review happens on demand via "stai review"
	}

	diff, err := git.StagedDiff()
	if err != nil {
		logf("hook pre-commit: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook pre-commit: nothing staged, passing")
		return
	}

	repoDir, err := os.Getwd()
	if err != nil {
		logf("hook pre-commit: getwd failed: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, repoDir, diff, false, false, "")
	if err != nil {
		logf("hook pre-commit: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("review_failed_commit")+"\n", err)
		return
	}
	logf("hook pre-commit done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, i18n.T("review_blocked_commit"))
		fmt.Fprint(os.Stderr, renderFindings(findings, true, true))
		os.Exit(1)
	}
}

// hookPrePush implements the strict-mode pre-push gate (M3-B). It only
// acts when pre_push.strict is enabled: the branch diff against
// pre_push.base_ref is reviewed and the push is blocked (exit 1) when a
// high-severity finding is present. AI or parse failures never block a push.
func hookPrePush() {
	if os.Getenv("STAI_DISABLE") != "" {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	setLangFromConfig(cfg)
	logPath = cfg.Log.Path
	logf("hook pre-push start strict=%v base=%s", cfg.PrePush.Strict, cfg.PrePush.BaseRef)
	if !cfg.PrePush.Strict {
		return // advisory branch review happens on demand via "stai review-branch"
	}

	diff, err := git.BranchDiffDir(".", cfg.PrePush.BaseRef)
	if err != nil {
		logf("hook pre-push: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		logf("hook pre-push: no commits ahead of %s", cfg.PrePush.BaseRef)
		return
	}

	repoDir, err := os.Getwd()
	if err != nil {
		logf("hook pre-push: getwd failed: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Hook.TimeoutSec)*time.Second)
	defer cancel()
	findings, err := generateReview(ctx, cfg, repoDir, diff, false, false, "")
	if err != nil {
		logf("hook pre-push: %v (passing)", err)
		fmt.Fprintf(os.Stderr, "stai: "+i18n.T("review_failed_push")+"\n", err)
		return
	}
	logf("hook pre-push done findings=%d high=%v", len(findings), ai.HasHigh(findings))
	if ai.HasHigh(findings) {
		fmt.Fprintln(os.Stderr, i18n.T("review_blocked_push"))
		fmt.Fprint(os.Stderr, renderFindings(findings, true, true))
		os.Exit(1)
	}
}