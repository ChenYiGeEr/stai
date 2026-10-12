package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stai/internal/ai"
	"stai/internal/config"
	"stai/internal/git"
	"stai/internal/i18n"
)

// cmdReview implements "stai review": AI review of the staged diff (M2).
// Advisory by default; -strict (or review.strict in the config) makes it
// exit non-zero when a high-severity finding is present. The report goes to
// stdout; when triggered from SourceTree (stdout discarded) a notification
// carries either the full report (few findings) or a summary plus the
// report-file path.
func cmdReview(args []string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	strictFlag := fs.Bool("strict", false, "exit non-zero on high-severity findings (pre-commit mode)")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	noCacheFlag := fs.Bool("no-cache", false, "bypass the local review cache")
	noValidateFlag := fs.Bool("no-validate-locations", false, "skip file/line validation of findings")
	formatFlag := fs.String("format", "text", "output format: text or json")
	outputFlag := fs.String("output", "", "write output to file instead of stdout (json format only)")
	resumeFlag := fs.String("resume", "", "resume an interrupted review by session id")
	fs.Parse(args)
	if *formatFlag != "text" && *formatFlag != "json" {
		fatal(fmt.Errorf("invalid -format %q (want text or json)", *formatFlag))
	}

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}
	reviewBaseURL, _, reviewModel, reviewTemperature := reviewProvider(cfg)
	logf("review start args=%q model=%s base_url=%s temperature=%v", args, reviewModel, reviewBaseURL, reviewTemperature)

	diff, err := git.StagedDiffDir(repoDir)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintln(os.Stderr, "stai: "+i18n.T("staged_empty"))
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff, *noCacheFlag, *noValidateFlag, *resumeFlag)
	var code int
	if *formatFlag == "json" {
		code = presentReviewJSON(findings, err, *outputFlag, "review")
	} else {
		code = presentReview(cfg, repoDir, findings, err, *noColorFlag, "review")
	}
	if code != 0 {
		os.Exit(code)
	}

	if (*strictFlag || cfg.Review.Strict) && ai.HasHigh(findings) {
		logf("review blocked commit (strict, high severity)")
		os.Exit(1)
	}
}

// reviewProvider resolves the effective endpoint for review: the review.*
// config overrides win over the global [provider], so review can use a
// stronger model while gen stays on a fast one.
func reviewProvider(cfg config.Config) (baseURL, apiKey, model string, temperature float64) {
	baseURL, apiKey, model = cfg.Provider.BaseURL, cfg.Provider.APIKey, cfg.Provider.Model
	temperature = cfg.Provider.Temperature
	if cfg.Review.BaseURL != "" {
		baseURL = cfg.Review.BaseURL
	}
	if cfg.Review.APIKey != "" {
		apiKey = cfg.Review.APIKey
	}
	if cfg.Review.Model != "" {
		model = cfg.Review.Model
	}
	if cfg.Review.Temperature != nil {
		temperature = *cfg.Review.Temperature
	}
	return baseURL, apiKey, model, temperature
}

// generateReview asks the configured model to review diff. Both the review
// command and the pre-commit hook use it so provider wiring lives in one place.
func generateReview(ctx context.Context, cfg config.Config, diff []byte, noCache, noValidateLocations bool, resumeID string) ([]ai.Finding, error) {
	baseURL, apiKey, model, temperature := reviewProvider(cfg)
	client := ai.NewClient(baseURL, apiKey, model,
		time.Duration(cfg.Provider.TimeoutSec)*time.Second)
	client.Temperature = temperature
	client.Logf = logf

	cachePath := ""
	if cfg.Review.Cache {
		if root, err := git.RepoRoot(); err == nil {
			cachePath, err = ai.ReviewCachePath(root)
			if err != nil {
				logf("review cache disabled: %v", err)
				cachePath = ""
			}
		} else {
			logf("review cache disabled: %v", err)
		}
	}

	sessionPath := ""
	if root, err := git.RepoRoot(); err == nil {
		if sp, serr := ai.ReviewSessionPath(root); serr == nil {
			sessionPath = sp
		} else {
			logf("review sessions disabled: %v", serr)
		}
	} else {
		logf("review sessions disabled: %v", err)
	}

	return client.GenerateReview(ctx, ai.ReviewParams{
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
		NoCache:           noCache,
		SessionPath:       sessionPath,
		ResumeID:          resumeID,
		ValidateLocations: cfg.Review.ValidateLocations && !noValidateLocations,
		CachePath:         cachePath,
		Logf:              logf,
	})
}

var severityColor = map[string]string{
	"high":   "\033[31m", // red
	"medium": "\033[33m", // yellow
	"low":    "\033[36m", // cyan
	"":       "\033[90m", // gray for advisory
}

const colorReset = "\033[0m"

// renderFindings formats findings as a readable report; empty means OK.
// When color is true, severity labels get ANSI color codes (suitable for
// terminal and SourceTree's full-output window). includeFixes controls whether
// SuggestedFix lines are printed (yes for stdout/report, no for notifications).
func renderFindings(findings []ai.Finding, color, includeFixes bool) string {
	if len(findings) == 0 {
		return i18n.T("review_ok") + "\n"
	}
	var b strings.Builder
	for _, f := range findings {
		sev := strings.ToUpper(f.Severity)
		if sev == "" {
			sev = "?"
		}
		prefix := fmt.Sprintf("[%s]", sev)
		if color {
			if c := severityColor[strings.ToLower(f.Severity)]; c != "" {
				prefix = c + prefix + colorReset
			}
		}
		if f.Location != "" {
			fmt.Fprintf(&b, "%s %s - %s\n", prefix, f.Location, f.Message)
		} else {
			fmt.Fprintf(&b, "%s %s\n", prefix, f.Message)
		}
		if includeFixes && f.SuggestedFix != "" {
			fmt.Fprintf(&b, "  %s%s\n", i18n.T("review_fix_label"), f.SuggestedFix)
		}
	}
	return b.String()
}

// severitySummary counts findings per severity level.
func severitySummary(findings []ai.Finding) string {
	var high, medium, low, other int
	for _, f := range findings {
		sev := strings.ToLower(f.Severity)
		switch sev {
		case "high":
			high++
		case "medium":
			medium++
		case "low":
			low++
		default:
			other++
		}
	}
	return fmt.Sprintf(i18n.T("review_summary"), high, medium, low, other)
}

// presentReview renders the review report to stdout, sends the notification,
// and returns the desired exit code. If err is non-nil but partial findings
// were returned, the partial results are shown with an incomplete warning and
// the function returns 1.
func presentReview(cfg config.Config, repoDir string, findings []ai.Finding, err error, noColor bool, command string) int {
	if err != nil && len(findings) == 0 {
		logf("%s error: %v", command, err)
		fmt.Fprintf(os.Stderr, "stai: %v\n", err)
		return 1
	}

	incomplete := err != nil
	var b strings.Builder
	if incomplete {
		fmt.Fprintln(&b, yellowText(i18n.T("review_incomplete_header"), !noColor))
	}
	b.WriteString(renderFindings(findings, !noColor, true))
	if incomplete {
		fmt.Fprintln(&b, yellowText(fmt.Sprintf(i18n.T("review_incomplete_footer"), err), !noColor))
	}
	fmt.Print(b.String())

	// Notifications must stay plain-text and concise.
	plainReport := renderFindings(findings, false, false)
	notifBody := plainReport
	subtitle := i18n.T("review_subtitle_done")
	if incomplete || len(findings) > cfg.Review.NotifyMaxFindings {
		if path, werr := writeReportFile(cfg, repoDir, findings); werr == nil {
			notifBody = fmt.Sprintf(i18n.T("review_report_notice"), path, severitySummary(findings))
			if incomplete {
				subtitle = i18n.T("review_subtitle_incomplete")
			} else {
				subtitle = i18n.T("review_subtitle_file")
			}
		} else {
			logf("%s report file write failed: %v", command, werr)
			notifBody = severitySummary(findings)
			if incomplete {
				subtitle = i18n.T("review_subtitle_incomplete")
			}
		}
	}
	if incomplete {
		notifBody = i18n.T("review_incomplete_header") + "\n" + notifBody
	}
	notify(cfg.Notify.Title, subtitle, notifBody)

	if incomplete {
		logf("%s incomplete findings=%d high=%v error=%v", command, len(findings), ai.HasHigh(findings), err)
		return 1
	}
	logf("%s done findings=%d high=%v", command, len(findings), ai.HasHigh(findings))
	return 0
}

// yellowText returns s in yellow when color is enabled, otherwise plain s.
func yellowText(s string, enabled bool) string {
	if enabled {
		return "\033[33m" + s + "\033[0m"
	}
	return s
}

// reviewJSONFinding is the machine-readable form of one finding. It mirrors
// ai.Finding but with stable JSON tags so the output schema does not drift
// with Go field renames.
type reviewJSONFinding struct {
	Severity     string `json:"severity"`
	Location     string `json:"location,omitempty"`
	Message      string `json:"message"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
}

// reviewJSONOutput is the top-level JSON result of a review run.
type reviewJSONOutput struct {
	Complete bool                `json:"complete"`
	Error    string              `json:"error,omitempty"`
	Findings []reviewJSONFinding `json:"findings"`
}

// buildReviewJSONOutput mirrors presentReviewJSON's schema without I/O so
// both the CLI and the MCP server share one shape.
func buildReviewJSONOutput(findings []ai.Finding, err error) reviewJSONOutput {
	out := reviewJSONOutput{Complete: err == nil, Findings: make([]reviewJSONFinding, 0, len(findings))}
	if err != nil {
		out.Error = err.Error()
	}
	for _, f := range findings {
		out.Findings = append(out.Findings, reviewJSONFinding{
			Severity:     f.Severity,
			Location:     f.Location,
			Message:      f.Message,
			SuggestedFix: f.SuggestedFix,
		})
	}
	return out
}

// presentReviewJSON renders findings as JSON. When output is non-empty the
// JSON is written to that file and a one-line human summary goes to stdout;
// otherwise the JSON goes to stdout and nothing else is printed.
// Notifications and the markdown report file are skipped in JSON mode.
func presentReviewJSON(findings []ai.Finding, err error, output, command string) int {
	out := buildReviewJSONOutput(findings, err)
	data, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		fmt.Fprintf(os.Stderr, "stai: %v\n", e)
		return 1
	}
	if output != "" {
		if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "stai: %v\n", err)
			return 1
		}
		fmt.Printf(i18n.T("review_json_saved")+"\n", output)
	} else {
		fmt.Println(string(data))
	}
	logf("%s json output complete=%v findings=%d", command, out.Complete, len(findings))
	if !out.Complete {
		return 1
	}
	return 0
}

// writeReportFile saves the full report under the repo (default
// .git/stai-review.md, configurable via review.report_path) and returns the
// written path.
func writeReportFile(cfg config.Config, repoDir string, findings []ai.Finding) (string, error) {
	path := cfg.Review.ReportPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoDir, path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf(i18n.T("review_report_title")+"%s\n%s\n",
		time.Now().Format("2006-01-02 15:04:05"), renderFindings(findings, false, true))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// cmdReviewBranch implements "stai review-branch": AI review of the branch
// diff against pre_push.base_ref (M3-B). Advisory by default; -strict makes
// it exit non-zero on high-severity findings.
func cmdReviewBranch(args []string) {
	fs := flag.NewFlagSet("review-branch", flag.ExitOnError)
	strictFlag := fs.Bool("strict", false, "exit non-zero on high-severity findings")
	noColorFlag := fs.Bool("no-color", false, "disable ANSI colors in output")
	noCacheFlag := fs.Bool("no-cache", false, "bypass the local review cache")
	noValidateFlag := fs.Bool("no-validate-locations", false, "skip file/line validation of findings")
	formatFlag := fs.String("format", "text", "output format: text or json")
	outputFlag := fs.String("output", "", "write output to file instead of stdout (json format only)")
	resumeFlag := fs.String("resume", "", "resume an interrupted review by session id")
	fs.Parse(args)
	if *formatFlag != "text" && *formatFlag != "json" {
		fatal(fmt.Errorf("invalid -format %q (want text or json)", *formatFlag))
	}

	cfg := loadConfig()

	repoDir := ""
	if fs.NArg() > 0 {
		repoDir = fs.Arg(0)
	}
	if repoDir == "" {
		var err error
		repoDir, err = os.Getwd()
		fatal(err)
	}

	baseRef := cfg.PrePush.BaseRef
	reviewBaseURL, _, reviewModel, reviewTemperature := reviewProvider(cfg)
	logf("review-branch start args=%q model=%s base_url=%s base=%s temperature=%v", args, reviewModel, reviewBaseURL, baseRef, reviewTemperature)

	diff, err := git.BranchDiffDir(repoDir, baseRef)
	fatal(err)
	if len(bytes.TrimSpace(diff)) == 0 {
		fmt.Fprintf(os.Stderr, i18n.T("ahead_none")+"\n", baseRef)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	findings, err := generateReview(ctx, cfg, diff, *noCacheFlag, *noValidateFlag, *resumeFlag)
	var code int
	if *formatFlag == "json" {
		code = presentReviewJSON(findings, err, *outputFlag, "review-branch")
	} else {
		code = presentReview(cfg, repoDir, findings, err, *noColorFlag, "review-branch")
	}
	if code != 0 {
		os.Exit(code)
	}

	if (*strictFlag || cfg.PrePush.Strict) && ai.HasHigh(findings) {
		logf("review-branch blocked push (strict, high severity)")
		os.Exit(1)
	}
}