// Command stai is the AI companion for SourceTree git workflows.
// review-loop-gen implements the automated fix-review cycle.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

// cmdReviewLoopGen implements "stai review-loop-gen": run an automated
// review → fix → review loop until the review passes or limits are hit.
func cmdReviewLoopGen(args []string) {
	fs := flag.NewFlagSet("review-loop-gen", flag.ExitOnError)
	maxIterFlag := fs.Int("max-iterations", 0, "maximum review-fix iterations (0 = config default)")
	timeoutFlag := fs.Int("timeout", 0, "overall timeout in seconds (0 = config default)")
	strictFlag := fs.Bool("strict", false, "treat LOW findings as failures")
	fixPromptFlag := fs.String("fix-prompt", "", "custom fix prompt template file")
	mcpTimeoutFlag := fs.Int("mcp-timeout", 0, "per-fix-call MCP timeout in seconds (0 = config default)")
	noMCPFallbackFlag := fs.Bool("no-mcp-fallback", false, "fail if MCP client unavailable instead of manual fallback")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	verboseFlag := fs.Bool("v", false, "verbose output: print findings and diffs each iteration")
	fs.Parse(args)

	cfg := loadConfig()

	// Override config with flags
	if *maxIterFlag > 0 {
		cfg.ReviewLoop.MaxIterations = *maxIterFlag
	}
	if *timeoutFlag > 0 {
		cfg.ReviewLoop.Timeout = *timeoutFlag
	}
	if *strictFlag {
		cfg.ReviewLoop.Strict = true
	}
	if *fixPromptFlag != "" {
		cfg.ReviewLoop.FixPromptPath = *fixPromptFlag
	}
	if *mcpTimeoutFlag > 0 {
		cfg.ReviewLoop.MCPTimeout = *mcpTimeoutFlag
	}
	if *noMCPFallbackFlag {
		cfg.ReviewLoop.NoMCPFallback = true
	}

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	// Cap iterations at 100 to prevent runaway loops
	if cfg.ReviewLoop.MaxIterations <= 0 {
		cfg.ReviewLoop.MaxIterations = 10
	}
	if cfg.ReviewLoop.MaxIterations > 100 {
		cfg.ReviewLoop.MaxIterations = 100
	}

	// Overall timeout context
	ctx := context.Background()
	var cancel context.CancelFunc
	if cfg.ReviewLoop.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.ReviewLoop.Timeout)*time.Second)
		defer cancel()
	}

	logf("review-loop-gen start repo=%s max_iter=%d timeout=%ds strict=%v mcp_timeout=%ds",
		repoDir, cfg.ReviewLoop.MaxIterations, cfg.ReviewLoop.Timeout, cfg.ReviewLoop.Strict, cfg.ReviewLoop.MCPTimeout)

	// Load custom fix prompt if provided
	fixPrompt := defaultFixPrompt
	if cfg.ReviewLoop.FixPromptPath != "" {
		data, err := os.ReadFile(cfg.ReviewLoop.FixPromptPath)
		if err != nil {
			fatal(fmt.Errorf("reading fix prompt: %w", err))
		}
		fixPrompt = string(data)
	}

	// Run the loop
	iter := 0
	var lastFindings []ai.Finding
	var lastErr error

	for iter < cfg.ReviewLoop.MaxIterations {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "review-loop-gen: overall timeout after %d iterations\n", iter)
			os.Exit(1)
		default:
		}

		iter++
		iterStart := time.Now()

		// Stage changes (git add -u)
		if err := gitAddU(repoDir); err != nil {
			fatal(fmt.Errorf("git add -u: %w", err))
		}

		// Get staged diff
		diff, err := git.StagedDiffDir(repoDir)
		if err != nil {
			fatal(fmt.Errorf("staged diff: %w", err))
		}
		if len(bytes.TrimSpace(diff)) == 0 {
			fmt.Fprintln(os.Stderr, i18n.T("review_loop_no_changes"))
			// No changes to review, generate commit message and exit
			runGen(ctx, repoDir, cfg)
			return
		}

		// Run review
		findings, reviewErr := runReview(ctx, cfg, repoDir, diff, *verboseFlag, *noColorFlag)
		if reviewErr != nil && len(findings) == 0 {
			// Hard error, no partial findings
			logf("review-loop-gen iter %d: review error: %v", iter, reviewErr)
			fmt.Fprintf(os.Stderr, "stai: %v\n", reviewErr)
			os.Exit(1)
		}

		lastFindings = findings
		lastErr = reviewErr

		// Check if review passed
		if reviewPassed(findings, cfg.ReviewLoop.Strict) {
			logf("review-loop-gen iter %d: review passed (%d findings)", iter, len(findings))
			fmt.Fprintf(os.Stderr, "✓ Review passed after %d iteration(s)\n", iter)
			// Generate commit message
			runGen(ctx, repoDir, cfg)
			return
		}

		// Review has findings - print summary
		if *verboseFlag {
			fmt.Fprintln(os.Stderr, renderFindings(findings, !*noColorFlag, true))
		} else {
			fmt.Fprintf(os.Stderr, "Iter %d: %s\n", iter, severitySummary(findings))
		}

		// Try to get fix via MCP
		fixDiff, fixErr := requestFixViaMCP(ctx, cfg, diff, findings, fixPrompt, *verboseFlag)
		if fixErr != nil {
			logf("review-loop-gen iter %d: fix request failed: %v", iter, fixErr)

			if cfg.ReviewLoop.NoMCPFallback {
				fmt.Fprintf(os.Stderr, "stai: fix request failed: %v\n", fixErr)
				os.Exit(1)
			}

			// Manual fallback: print findings and wait for user
			fmt.Fprintf(os.Stderr, "%s\n", i18n.T("review_loop_manual_fix"))
			fmt.Fprintln(os.Stderr, renderFindings(findings, !*noColorFlag, true))
			fmt.Fprint(os.Stderr, i18n.T("review_loop_press_enter"))
			_, _ = fmt.Scanln()
			continue
		}

		// Apply the fix
		if len(bytes.TrimSpace(fixDiff)) > 0 {
			if err := applyDiff(repoDir, fixDiff); err != nil {
				logf("review-loop-gen iter %d: apply diff failed: %v", iter, err)
				fmt.Fprintf(os.Stderr, "stai: applying fix failed: %v\n", err)

				if cfg.ReviewLoop.NoMCPFallback {
					os.Exit(1)
				}

				fmt.Fprintf(os.Stderr, "%s\n", i18n.T("review_loop_apply_failed"))
				fmt.Fprint(os.Stderr, i18n.T("review_loop_press_enter"))
				_, _ = fmt.Scanln()
				continue
			}
			if *verboseFlag {
				fmt.Fprintf(os.Stderr, "Applied fix (%d bytes)\n", len(fixDiff))
			}
		}

		logf("review-loop-gen iter %d done in %v findings=%d", iter, time.Since(iterStart), len(findings))
	}

	// Max iterations reached without passing
	fmt.Fprintf(os.Stderr, "review-loop-gen: max iterations (%d) reached, review still has findings\n", cfg.ReviewLoop.MaxIterations)
	if len(lastFindings) > 0 {
		fmt.Fprintln(os.Stderr, renderFindings(lastFindings, !*noColorFlag, true))
	}
	if lastErr != nil {
		fmt.Fprintf(os.Stderr, "Last error: %v\n", lastErr)
	}
	os.Exit(1)
}

// runReview runs a single review and returns findings and any error.
func runReview(ctx context.Context, cfg config.Config, repoDir string, diff []byte, verbose, noColor bool) ([]ai.Finding, error) {
	reviewBaseURL, _, reviewModel, reviewTemperature := reviewProvider(cfg)
	logf("review-loop-gen review model=%s base_url=%s temperature=%v", reviewModel, reviewBaseURL, reviewTemperature)

	cachePath := ""
	if cfg.Review.Cache {
		if root, err := git.RepoRootAt(repoDir); err == nil {
			cachePath, err = ai.ReviewCachePath(root)
			if err != nil {
				logf("review cache disabled: %v", err)
				cachePath = ""
			}
		}
	}

	sessionPath := ""
	if root, err := git.RepoRootAt(repoDir); err == nil {
		if sp, serr := ai.ReviewSessionPath(root); serr == nil {
			sessionPath = sp
		}
	}

	client := ai.NewClient(reviewBaseURL, cfg.Provider.APIKey, reviewModel,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = reviewTemperature
	client.Logf = logf

	findings, err := client.GenerateReview(ctx, ai.ReviewParams{
		Diff:              diff,
		MaxChars:          cfg.Commit.MaxDiffChars,
		Retries:           cfg.Commit.Retries,
		GroupMaxLines:     cfg.Review.GroupMaxLines,
		Concurrency:       cfg.Review.Concurrency,
		Timeout:           cfg.Provider.TimeoutSec,
		Rules:             cfg.Review.Rules,
		RuleSets:          cfg.Review.RuleSets,
		Bundle:            cfg.Review.Bundle,
		SuggestFixes:      cfg.Review.SuggestFixes,
		Strict:            cfg.Review.Strict,
		Deterministic:     cfg.Review.Deterministic,
		Seed:              cfg.Review.Seed,
		Cache:             cfg.Review.Cache,
		NoCache:           false,
		SessionPath:       sessionPath,
		ResumeID:          "",
		ValidateLocations: cfg.Review.ValidateLocations,
		CachePath:         cachePath,
		Logf:              logf,
	})

	return findings, err
}

// reviewPassed returns true if the review has no blocking findings.
func reviewPassed(findings []ai.Finding, strict bool) bool {
	if len(findings) == 0 {
		return true
	}
	if strict {
		return false // any finding blocks in strict mode
	}
	// Non-strict: only HIGH/MEDIUM block (case-insensitive)
	for _, f := range findings {
		sev := strings.ToLower(f.Severity)
		if sev == "high" || sev == "medium" {
			return false
		}
	}
	return true
}

// requestFixViaMCP sends the findings and diff to the MCP server (via stdin/stdout)
// and requests a unified diff fix. Returns the fix diff or an error.
func requestFixViaMCP(ctx context.Context, cfg config.Config, diff []byte, findings []ai.Finding, fixPrompt string, verbose bool) ([]byte, error) {
	// Use MCPTimeout if configured, otherwise fall back to Provider.TimeoutSec
	timeoutSec := cfg.Provider.TimeoutSec
	if cfg.ReviewLoop.MCPTimeout > 0 {
		timeoutSec = cfg.ReviewLoop.MCPTimeout
	}
	fixCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	return generateFixDirectly(fixCtx, cfg, diff, findings, fixPrompt, verbose, timeoutSec)
}

// generateFixDirectly uses the AI client to generate a fix diff directly.
// This bypasses MCP for now since the review_diff tool only does review.
func generateFixDirectly(ctx context.Context, cfg config.Config, diff []byte, findings []ai.Finding, fixPrompt string, verbose bool, timeoutSec int) ([]byte, error) {
	reviewBaseURL, _, reviewModel, reviewTemperature := reviewProvider(cfg)

	client := ai.NewClient(reviewBaseURL, cfg.Provider.APIKey, reviewModel,
		time.Duration(timeoutSec)*time.Second)
	client.Temperature = reviewTemperature
	client.Logf = logf

	// Convert findings to a summary string
	var findingsSummary strings.Builder
	for _, f := range findings {
		findingsSummary.WriteString(fmt.Sprintf("[%s] %s - %s\n", strings.ToUpper(f.Severity), f.Location, f.Message))
		if f.SuggestedFix != "" {
			findingsSummary.WriteString(fmt.Sprintf("  Fix suggestion: %s\n", f.SuggestedFix))
		}
	}

	// Build the fix prompt
	fullPrompt := strings.ReplaceAll(fixPrompt, "{{DIFF}}", string(diff))
	fullPrompt = strings.ReplaceAll(fullPrompt, "{{FINDINGS}}", findingsSummary.String())

	// Use the AI client's Chat method to generate a fix
	// System prompt instructs the model to output ONLY a valid unified diff
	systemPrompt := `You are an expert code fixer. Given a diff and a list of review findings, produce a unified diff that fixes all the issues.
Requirements:
- Output ONLY a valid unified diff (no markdown, no explanation, no code fences)
- Fix all HIGH and MEDIUM severity issues
- LOW severity issues are optional but preferred
- Do not change unrelated code
- Keep changes minimal and focused
- The diff must apply cleanly with "git apply"`

	response, err := client.Chat(ctx, systemPrompt, fullPrompt)
	if err != nil {
		return nil, fmt.Errorf("fix generation failed: %w", err)
	}

	// Extract the diff from the response (strip any code fences if present)
	fixDiff := extractDiff(response)
	if len(bytes.TrimSpace(fixDiff)) == 0 {
		return nil, fmt.Errorf("model returned empty diff")
	}

	return fixDiff, nil
}

// extractDiff extracts a unified diff from the model's response, handling
// optional markdown code fences.
func extractDiff(response string) []byte {
	// Try to find a diff inside markdown code fences
	lines := strings.Split(response, "\n")
	inDiff := false
	sawFence := false
	var diffLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			inDiff = !inDiff
			sawFence = true
			continue
		}
		// Only collect lines that look like unified diff content
		// If we saw a fence, only collect inside the fence; otherwise collect all diff-like lines
		if !sawFence || inDiff {
			if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\") {
				diffLines = append(diffLines, line)
			}
		}
	}
	if len(diffLines) > 0 {
		return []byte(strings.Join(diffLines, "\n"))
	}
	// Fallback: return the whole response
	return []byte(response)
}

// buildFixPrompt constructs the prompt for fix generation.
func buildFixPrompt(template string, diff []byte, findings []ai.Finding) string {
	// Template variables: {{DIFF}}, {{FINDINGS}}
	return template
}

// findingsToJSON converts findings to JSON-serializable format.
func findingsToJSON(findings []ai.Finding) []map[string]any {
	out := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		m := map[string]any{
			"severity": f.Severity,
			"message":  f.Message,
		}
		if f.Location != "" {
			m["location"] = f.Location
		}
		if f.SuggestedFix != "" {
			m["suggested_fix"] = f.SuggestedFix
		}
		out = append(out, m)
	}
	return out
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// gitAddU runs `git add -u` in the repo directory.
func gitAddU(repoDir string) error {
	cmd := exec.Command("git", "add", "-u")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add -u: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyDiff applies a unified diff to the repository.
func applyDiff(repoDir string, diff []byte) error {
	// Write diff to a temp file and use git apply --index to stage new files
	tmpFile, err := os.CreateTemp("", "stai-fix-*.patch")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(diff); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	cmd := exec.Command("git", "apply", "--index", "--whitespace=nowarn", tmpFile.Name())
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git apply: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runGen runs the gen command to generate a commit message.
func runGen(ctx context.Context, repoDir string, cfg config.Config) {
	diff, err := git.StagedDiffDir(repoDir)
	if err != nil {
		logf("review-loop-gen: gen diff error: %v", err)
		return
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		return
	}
	msg, err := generate(ctx, cfg, diff)
	if err != nil {
		logf("review-loop-gen: gen error: %v", err)
		return
	}
	if err := writeClipboard(msg); err != nil {
		logf("review-loop-gen: clipboard write failed: %v", err)
	}
	notifyCopied(cfg.Notify, msg)
	fmt.Fprintln(os.Stderr, i18n.T("copied"))
	fmt.Println(msg)
}

// defaultFixPrompt is the built-in fix prompt template.
const defaultFixPrompt = `You are an expert code reviewer and fixer. Given a diff and a list of review findings, produce a unified diff that fixes all the issues.

The diff to fix:
{{DIFF}}

The review findings:
{{FINDINGS}}

Requirements:
- Output ONLY a valid unified diff (no markdown, no explanation)
- Fix all HIGH and MEDIUM severity issues
- LOW severity issues are optional but preferred
- Do not change unrelated code
- Keep changes minimal and focused
- The diff must apply cleanly with "git apply"

Unified diff output:
`
