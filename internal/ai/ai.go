// Package ai talks to an OpenAI-compatible chat completions endpoint
// (Ollama, LM Studio, api gateways, ...) and builds commit messages on top
// of it. The provider is plain HTTP+JSON so stai ships with zero
// third-party dependencies.
package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"stai/internal/config"
	"stai/internal/i18n"
)

// lang is the output language for prompts, validation and user-facing
// errors: "zh-CN" or "en". It is set once via SetLang after config load;
// the zero value is zh-CN. Prompts keep per-language templates below.
var lang = "zh-CN"

// SetLang sets the output language. Must be called before generating.
func SetLang(l string) { lang = l }

func langIsEnglish() bool { return lang == "en" }

type Client struct {
	baseURL     string
	apiKey      string
	model       string
	http        *http.Client
	sessionID   string                           // one per run, so providers can group a conversation
	Temperature float64                          // request temperature; 0 = deterministic. Some models only accept 1 — set it via config.
	Logf        func(format string, args ...any) // optional debug log; nil means silent
}

func NewClient(baseURL, apiKey, model string, timeout time.Duration) *Client {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		model:     model,
		http:      &http.Client{Timeout: timeout},
		sessionID: hex.EncodeToString(buf),
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends a single-turn conversation and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	return c.chat(ctx, []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
}

func (c *Client) chat(ctx context.Context, messages []chatMessage) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.Temperature,
		Stream:      false,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stai/1.0")
	req.Header.Set("x-opencode-session", c.sessionID)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decoding provider response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("provider error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("provider returned an empty completion")
	}
	return out.Choices[0].Message.Content, nil
}

// GenerateCommit asks the model for a commit message for the given diff and
// normalizes the reply (models love wrapping output in code fences). A reply
// that violates the rules is sent back in the same conversation with the
// failure reason, up to cfg.Retries times. If it is still invalid, an error
// is returned so the caller can tell the user instead of using a bad message.
func (c *Client) GenerateCommit(ctx context.Context, cfg config.Commit, diff []byte) (string, error) {
	body := string(diff)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(body) > cfg.MaxDiffChars {
		body = truncateUTF8(body, cfg.MaxDiffChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}

	files, changedLines := diffStats(diff)
	messages := []chatMessage{
		{Role: "system", Content: buildCommitSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\n%s%s",
			diffStatsLine(files, changedLines), commitUserLead(), body, truncated)},
	}

	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	msg := cleanMessage(raw)
	reason := validateCommit(msg, cfg)

	for attempt := 0; reason != "" && attempt < cfg.Retries; attempt++ {
		messages = append(messages,
			chatMessage{Role: "assistant", Content: raw},
			chatMessage{Role: "user", Content: fmt.Sprintf(commitRetryLead(), reason)},
		)
		raw, err = c.chat(ctx, messages)
		if err != nil {
			return "", err
		}
		msg = cleanMessage(raw)
		reason = validateCommit(msg, cfg)
	}
	if reason != "" {
		return "", fmt.Errorf("%s: %s", i18n.T("err_commit_invalid"), reason)
	}
	return msg, nil
}

// diffStatsLine tells the model how many files and lines changed, in the
// active language.
func diffStatsLine(files, changedLines int) string {
	if langIsEnglish() {
		return fmt.Sprintf("Stats: %d files, %d changed lines (additions + deletions).", files, changedLines)
	}
	return fmt.Sprintf("改动统计:%d 个文件,%d 行变更(增删合计)。", files, changedLines)
}

// GeneratePR generates a one-line Conventional Commits title and the markdown
// PR description body in a SINGLE model call (F1 contract): the reply's
// first line is the title, a blank line follows, then the body with the
// ## Summary / ## Main changes sections. The title is validated like a
// commit subject and the body must contain "## " sections; non-conforming
// replies are retried per cfg.Retries.
func (c *Client) GeneratePR(ctx context.Context, cfg config.Commit, diff []byte) (title, body string, err error) {
	diffBody := string(diff)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(diffBody) > cfg.MaxDiffChars {
		if c.Logf != nil {
			c.Logf("pr diff truncated: %d -> %d chars", len(diffBody), cfg.MaxDiffChars)
		}
		diffBody = truncateUTF8(diffBody, cfg.MaxDiffChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}
	files, changedLines := diffStats(diff)
	messages := []chatMessage{
		{Role: "system", Content: prSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\n%s%s",
			diffStatsLine(files, changedLines), prUserLead(), diffBody, truncated)},
	}

	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", "", err
	}
	title, body, reason := splitPRReply(cleanMessage(raw), cfg)

	for attempt := 0; reason != "" && attempt < cfg.Retries; attempt++ {
		messages = append(messages,
			chatMessage{Role: "assistant", Content: raw},
			chatMessage{Role: "user", Content: fmt.Sprintf(prRetryLead(), reason)},
		)
		raw, err = c.chat(ctx, messages)
		if err != nil {
			return "", "", err
		}
		title, body, reason = splitPRReply(cleanMessage(raw), cfg)
	}
	if reason != "" {
		return "", "", fmt.Errorf("%s: %s", i18n.T("err_pr_invalid"), reason)
	}
	return title, body, nil
}

// splitPRReply cuts a model reply into its title line and description body
// per the F1 contract. It returns a short reason (in the active language)
// suitable for feeding back to the model when the reply does not conform.
func splitPRReply(msg string, cfg config.Commit) (title, body, reason string) {
	msg = strings.TrimSpace(msg)
	nl := strings.Index(msg, "\n")
	if nl < 0 {
		return "", "", prPartReason("structure")
	}
	title = strings.TrimSpace(msg[:nl])
	body = strings.TrimSpace(msg[nl+1:])
	if title == "" || body == "" {
		return "", "", prPartReason("structure")
	}
	if m := conventionalSubject.FindStringSubmatch(title); m == nil {
		if langIsEnglish() {
			return "", "", fmt.Sprintf("title line %q does not match the required type(scope): summary format", title)
		}
		return "", "", fmt.Sprintf("标题行 %q 不符合 type(scope): 概要 的格式", title)
	} else if !contains(cfg.Types, m[1]) {
		if langIsEnglish() {
			return "", "", fmt.Sprintf("type %q is not in the allowed list (allowed: %s)", m[1], strings.Join(cfg.Types, ", "))
		}
		return "", "", fmt.Sprintf("type %q 不在允许列表中（允许：%s）", m[1], strings.Join(cfg.Types, ", "))
	}
	if !langIsEnglish() && !containsCJK(title) {
		return "", "", "标题不是中文"
	}
	if !strings.Contains(body, "## ") {
		return "", "", prPartReason("sections")
	}
	return title, body, ""
}

// prPartReason returns the bilingual reason for the two structural checks in
// splitPRReply.
func prPartReason(kind string) string {
	if kind == "sections" {
		if langIsEnglish() {
			return "the description body must contain '## ' section headings"
		}
		return "描述正文必须包含 '## ' 小节标题"
	}
	if langIsEnglish() {
		return "the reply must be a title line, a blank line, then the description body"
	}
	return "回复必须是第一行标题、空一行、然后是描述正文"
}

func prRetryLead() string {
	if langIsEnglish() {
		return "The previous output violated the rules: %s. Regenerate strictly following the rules; output only the title line and the description."
	}
	return "上次输出不合规:%s。请严格按规则重新生成,只输出标题行和描述本身。"
}

func prUserLead() string {
	if langIsEnglish() {
		return "Generate the PR title (first line) and description for the following branch diff:"
	}
	return "请为以下分支 diff 生成 PR 标题(第一行)与描述:"
}

// GenerateStashMsg asks the model for a concise one-line stash message
// describing the working tree diff.
func (c *Client) GenerateStashMsg(ctx context.Context, cfg config.Commit, diff []byte) (string, error) {
	body := string(diff)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(body) > cfg.MaxDiffChars {
		body = truncateUTF8(body, cfg.MaxDiffChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}
	files, changedLines := diffStats(diff)
	messages := []chatMessage{
		{Role: "system", Content: stashMsgSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\n%s%s",
			diffStatsLine(files, changedLines), stashUserLead(), body, truncated)},
	}
	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	msg := strings.SplitN(cleanMessage(raw), "\n", 2)[0]
	if msg == "" {
		return "", fmt.Errorf("%s", i18n.T("err_empty_stash"))
	}
	return msg, nil
}

func stashUserLead() string {
	if langIsEnglish() {
		return "Generate a stash message for the following working-tree diff:"
	}
	return "为以下工作树 diff 生成 stash message:"
}

// Finding is one issue the model found in the staged diff.
type Finding struct {
	Severity     string // "high", "medium", "low" — "" means an unparsed advisory line
	Location     string // e.g. "internal/ai/ai.go:42"; may be empty
	Message      string
	SuggestedFix string // optional concrete fix/patch suggestion; may be empty
}

// HasHigh reports whether any finding is high severity.
func HasHigh(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == "high" {
			return true
		}
	}
	return false
}

// ReviewParams carries everything GenerateReview needs.
type ReviewParams struct {
	Diff          []byte
	MaxChars      int                              // truncate each group beyond this, telling the model
	Retries       int                              // parse-retry count per group (0 = validate the first reply only)
	GroupMaxLines int                              // diff is split into per-file groups whose combined changed lines stay under this; <= 0 = single group
	Concurrency   int                              // groups reviewed in parallel; <= 1 = serial
	Rules         []string                         // project-specific rules appended to the review prompt
	SuggestFixes  bool                             // ask the model for a concrete fix suggestion per finding
	Logf          func(format string, args ...any) // optional per-group progress log
}

var reviewFinding = regexp.MustCompile(`(?i)^\[(high|medium|low)\]\s*([^\s]+)\s*-\s*(.+)$`)

// GenerateReview asks the model to review the staged diff and returns the
// findings. The model must answer with one line per finding in the form
// "[high|medium|low] path:line - description", or "OK" when there is nothing
// to report. A reply that does not parse is retried (same conversation, the
// diff is not resent) with the reason fed back; lines that still do not parse
// are returned as advisory findings with an empty severity, so a broken
// reply is shown as advice instead of being dropped or blocking a commit.
//
// Large diffs are split into per-file groups (each group's combined changed
// lines stay under GroupMaxLines) that are reviewed one call at a time —
// small models miss issues in diffs that dilute their attention, but catch
// them when shown one file at a time. Findings from all groups are merged
// and sorted high → low.
func (c *Client) GenerateReview(ctx context.Context, p ReviewParams) ([]Finding, error) {
	groups := groupDiff(p.Diff, p.GroupMaxLines)
	if p.Concurrency > 1 && len(groups) > 1 {
		return c.reviewGroupsParallel(ctx, p, groups)
	}
	var all []Finding
	for i, g := range groups {
		findings, err := c.reviewGroup(ctx, p, g, i+1, len(groups))
		if err != nil {
			return nil, err
		}
		all = append(all, findings...)
	}
	sortFindings(all)
	return all, nil
}

// reviewGroupsParallel reviews the groups through a worker pool. Local
// single-GPU models mostly queue concurrent requests (no speedup, no harm);
// remote providers run truly in parallel, cutting wall time by the worker
// count. The first error that occurs cancels everything still queued or in
// flight (fast-fail, matching the serial mode) and is returned.
func (c *Client) reviewGroupsParallel(ctx context.Context, p ReviewParams, groups [][]byte) ([]Finding, error) {
	workers := p.Concurrency
	if workers > len(groups) {
		workers = len(groups)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([][]Finding, len(groups))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for i, g := range groups {
		wg.Add(1)
		go func(i int, g []byte) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done(): // a sibling failed before this group started
				return
			}
			defer func() { <-sem }()
			findings, err := c.reviewGroup(ctx, p, g, i+1, len(groups))
			results[i] = findings
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				cancel()
			}
		}(i, g)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	var all []Finding
	for _, findings := range results {
		all = append(all, findings...)
	}
	sortFindings(all)
	return all, nil
}

// reviewGroup reviews one diff group (a single call, with parse retries).
func (c *Client) reviewGroup(ctx context.Context, p ReviewParams, group []byte, num, total int) (findings []Finding, err error) {
	started := time.Now()
	defer func() {
		if p.Logf != nil {
			p.Logf("review group %d/%d done findings=%d elapsed=%s",
				num, total, len(findings), time.Since(started).Round(time.Millisecond))
		}
	}()
	diff := string(group)
	truncated := ""
	if p.MaxChars > 0 && len(diff) > p.MaxChars {
		diff = truncateUTF8(diff, p.MaxChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}

	prefix := ""
	if total > 1 {
		if langIsEnglish() {
			prefix = fmt.Sprintf("(group %d/%d) ", num, total)
		} else {
			prefix = fmt.Sprintf("(第 %d/%d 组) ", num, total)
		}
	}
	messages := []chatMessage{
		{Role: "system", Content: reviewPrompt(p.Rules, hasGoFiles(group), p.SuggestFixes)},
		{Role: "user", Content: fmt.Sprintf("%s%s\n\n%s%s", prefix, reviewUserLead(), diff, truncated)},
	}

	raw, err := c.chat(ctx, messages)
	if err != nil {
		return nil, err
	}
	findings, bad := parseReview(raw)

	for attempt := 0; len(bad) > 0 && attempt < p.Retries; attempt++ {
		fixHint := reviewRetryFixHint(p.SuggestFixes)
		// Start a fresh conversation that resends the diff (the model needs it
		// to reproduce the findings) instead of replaying the previous raw
		// reply — that would grow the context on every failed attempt.
		messages = []chatMessage{
			{Role: "system", Content: reviewPrompt(p.Rules, hasGoFiles(group), p.SuggestFixes)},
			{Role: "user", Content: fmt.Sprintf(reviewRetryLead(),
				strings.Join(bad, " / "), fixHint, prefix, diff, truncated)},
		}
		raw, err = c.chat(ctx, messages)
		if err != nil {
			return nil, err
		}
		// Accumulate instead of overwriting: a model may answer the feedback
		// with only the corrected lines, so round-1 findings must survive.
		newFindings, newBad := parseReview(raw)
		findings = mergeFindings(findings, newFindings)
		bad = newBad
	}
	// Still-unparsed lines become advisory findings (empty severity) so the
	// user still sees them; they never count as high severity.
	for _, line := range bad {
		findings = append(findings, Finding{Message: line})
	}
	return findings, nil
}

func reviewUserLead() string {
	if langIsEnglish() {
		return "Review the following staged diff:"
	}
	return "审查以下暂存 diff:"
}

// reviewRetryFixHint returns the suggest-fixes clause embedded in the retry
// instruction; empty when fixes are disabled.
func reviewRetryFixHint(suggestFixes bool) string {
	if !suggestFixes {
		return ""
	}
	if langIsEnglish() {
		return ";if a finding has a suggested fix, output '> Suggested fix: ...' on the line right after it"
	}
	return ";如某条问题有建议修改,请在该问题下一行输出 '> 建议修改: ...'"
}

// reviewRetryLead returns the retry user-message template: %q takes the
// unparsable lines, then %s the fix hint, %s the group prefix, %s the diff
// and %s the truncation note.
func reviewRetryLead() string {
	if langIsEnglish() {
		return "The previous output contained lines that could not be parsed: %q. Review again and output ALL findings in full (including the ones already printed correctly), one per line, in the format [high|medium|low] path:line - description%s; output only OK when there are none.\n\n%s" + reviewUserLead() + "\n\n%s%s"
	}
	return "上次输出存在无法解析的行:%q。请重新审查并完整输出全部问题(包括已正确输出的),每条一行,格式为 [high|medium|low] 文件路径:行号 - 问题描述%s;没有问题就只输出 OK。\n\n%s" + reviewUserLead() + "\n\n%s%s"
}

const reviewFixInstruction = `对每条问题,如果改法不明显,请在紧接着的下一行给出具体修改建议,格式为:
> 建议修改: <简要说明改哪一行、改成什么,或一段可直接参考的代码片段>
要求:建议必须是单行文本,不要换行;如需表达多个点,用分号分隔在同一行内。如果改法显而易见(如简单的变量重命名),可以省略建议行。`

const reviewFixInstructionEN = `For each finding, if the fix is not obvious, give a concrete suggestion on the line immediately after it, in the form:
> Suggested fix: <briefly state which line to change and to what, or a short code snippet to reference>
Requirements: the suggestion must be a single line — no line breaks; use semicolons to separate multiple points on that one line. Omit the suggestion line when the fix is obvious (e.g. a simple rename).`

// reviewPrompt assembles the system prompt: the base principles, a condensed
// Go checklist when the group touches Go files, any project rules, and the
// optional "suggest fixes" instruction.
func reviewPrompt(rules []string, hasGo, suggestFixes bool) string {
	var b strings.Builder
	b.WriteString(reviewSystemPromptForLang())
	if hasGo {
		b.WriteString("\n\n" + reviewGoChecklistForLang())
	}
	if len(rules) > 0 {
		if langIsEnglish() {
			b.WriteString("\n\nAdditional project rules (must follow):\n")
		} else {
			b.WriteString("\n\n项目附加规则(必须遵守):\n")
		}
		for _, r := range rules {
			b.WriteString("- " + r + "\n")
		}
	}
	if suggestFixes {
		if langIsEnglish() {
			b.WriteString("\n\n" + reviewFixInstructionEN)
		} else {
			b.WriteString("\n\n" + reviewFixInstruction)
		}
	}
	return b.String()
}

func reviewSystemPromptForLang() string {
	if langIsEnglish() {
		return reviewSystemPromptEN
	}
	return reviewSystemPrompt
}

func reviewGoChecklistForLang() string {
	if langIsEnglish() {
		return reviewGoChecklistEN
	}
	return reviewGoChecklist
}

// reviewSystemPrompt is the base review prompt. The first two principles are
// borrowed from alibaba/open-code-review: precision beats recall (a false
// positive costs reviewer trust), and findings that deterministic tooling
// (gofmt, go vet, Staticcheck, the compiler) can already surface are not
// reported.
const reviewSystemPrompt = `你是提交前 code review 审查器。严格遵守以下规则:
1. 精确优先于召回:只报告你能在 diff 及其上下文中确认的缺陷,不要猜测,不要报告风格偏好。误报会消耗审阅者的信任。
2. 不报告确定性工具能发现的问题:格式、未使用的变量/导入、编译器或 go vet、Staticcheck、gofmt 能查出的静态问题一律不报。
3. 只报告确实存在的问题:缺陷、安全隐患、逻辑错误、明显的性能问题。
4. 每条问题一行,格式为 [HIGH|MEDIUM|LOW] 文件路径:行号 - 问题描述。HIGH 仅用于会导致缺陷、数据丢失或安全漏洞的问题。
5. 问题描述用中文,一行一条,简短具体,指出改什么。
6. 没有任何问题时只输出 OK,不要输出其他任何内容。
7. 不要解释、不要代码围栏、不要任何前言或后缀。

示例(有 2 条问题):
[HIGH] internal/ai/ai.go:102 - err 被覆盖,底层的读取错误会丢失
[LOW] cmd/stai/main.go:40 - 超时常量重复定义了两次

示例(无问题):
OK`

// reviewSystemPromptEN is the English counterpart of reviewSystemPrompt.
// Severity tokens and OK stay English in both languages — they are the
// parser's contract.
const reviewSystemPromptEN = `You are a pre-commit code review inspector. Obey these rules strictly:
1. Precision beats recall: only report defects you can confirm in the diff and its context; do not guess, do not report style preferences. False positives cost reviewer trust.
2. Do not report what deterministic tooling already finds: formatting, unused variables/imports, compiler or go vet issues, Staticcheck, gofmt — none of these.
3. Only report real problems: defects, security risks, logic errors, obvious performance issues.
4. One finding per line, in the format [HIGH|MEDIUM|LOW] path:line - description. HIGH is only for issues that would cause defects, data loss or security holes.
5. Write the description in English, one line each, short and specific, pointing out what to change.
6. When there are no findings, output only OK — nothing else.
7. No explanations, no code fences, no preamble or trailing remarks.

Example (2 findings):
[HIGH] internal/ai/ai.go:102 - err is overwritten, losing the underlying read error
[LOW] cmd/stai/main.go:40 - the timeout constant is defined twice

Example (no findings):
OK`

// reviewGoChecklist is a condensed, model-sized version of the Go review
// principles from alibaba/open-code-review (internal/config/rules/rule_docs
// /go.md). The original assumes a cloud model with file_read/code_search
// tool access and would dilute a small local model, so only the
// diff-observable highlights are kept.
const reviewGoChecklist = `Go 专项关注点(只报确实存在于改动中的问题):
- 错误:被忽略或覆盖的错误;该保留 errors.Is/As 可判性时用了 %v;请求/库路径上的 panic 或 log.Fatal
- nil:nil map 写入会 panic;nil channel 收发永久阻塞;typed nil 存入接口后判空失效;构造函数或输入可达的指针未判空就解引用
- context:该继承调用者取消链/超时时用了 context.Background();WithCancel/Timeout 的 cancel 用完未调用
- goroutine:可能永久阻塞且无人能退出的 goroutine;闭包捕获循环变量或请求期的可变数据
- 并发:map/slice/字段的无锁并发读写;持锁做阻塞 I/O;channel 可能重复 close 或向已关闭 channel 发送
- 资源:文件/response.Body/rows 并非所有路径都关闭;循环内 defer 使资源延迟到函数返回才释放
- 边界:切片/数组索引在空输入或边界输入时越界;整型转换溢出或负数转无符号变成巨大值
- 安全:SQL/命令/路径由不可信输入拼接;math/rand 用于安全敏感随机数;密钥或凭据写入日志/错误信息`

// reviewGoChecklistEN is the English counterpart of reviewGoChecklist.
const reviewGoChecklistEN = `Go-specific focus (only report issues that truly exist in the change):
- errors: ignored or overwritten errors; using %v where errors.Is/As comparability must be preserved; panic or log.Fatal on request/library paths
- nil: writes to a nil map panic; send/receive on a nil channel blocks forever; typed nil stored in an interface breaks nil checks; pointers reachable from constructors or input dereferenced without a nil check
- context: context.Background() where the caller's cancellation chain/deadline should be inherited; cancel from WithCancel/WithTimeout not called
- goroutines: goroutines that can block forever with no way out; closures capturing loop variables or request-scoped mutable data
- concurrency: lock-free concurrent map/slice/field access; holding a lock across blocking I/O; channel that can be closed twice or sent on after close
- resources: files/response.Body/rows not closed on every path; defer in a loop delaying release until the function returns
- bounds: slice/array index out of range on empty or boundary input; integer conversion overflow or negative-to-unsigned becoming huge
- security: SQL/command/paths built from untrusted input; math/rand for security-sensitive randomness; secrets or credentials written to logs/errors`

// groupDiff splits a diff into review groups: each file becomes a chunk
// (split on "diff --git" headers), chunks are batched into groups whose
// combined changed-line count (added + removed, excluding file headers)
// stays <= maxLines. A single file always stays whole, even when it alone
// exceeds maxLines. maxLines <= 0, an unparseable diff, or a diff that fits
// in one group yields a single group.
func groupDiff(diff []byte, maxLines int) [][]byte {
	if maxLines <= 0 {
		return [][]byte{diff}
	}
	chunks := splitDiffFiles(diff)
	if len(chunks) <= 1 {
		return [][]byte{diff}
	}
	var groups [][]byte
	var cur strings.Builder
	curLines := 0
	flush := func() {
		if cur.Len() > 0 {
			groups = append(groups, []byte(cur.String()))
			cur.Reset()
			curLines = 0
		}
	}
	for _, ch := range chunks {
		n := changedLines(ch)
		if cur.Len() > 0 && curLines+n > maxLines {
			flush()
		}
		cur.Write(ch)
		curLines += n
	}
	flush()
	return groups
}

// splitDiffFiles cuts a diff into per-file chunks on "diff --git" headers,
// keeping each header with its chunk.
func splitDiffFiles(diff []byte) [][]byte {
	s := string(diff)
	var starts []int
	rest := s
	offset := 0
	for {
		i := strings.Index(rest, "\ndiff --git ")
		if i < 0 {
			break
		}
		starts = append(starts, offset+i+1)
		offset += i + 1
		rest = s[offset:]
	}
	if len(starts) == 0 {
		return [][]byte{diff}
	}
	var chunks [][]byte
	prev := 0
	for _, k := range starts {
		chunks = append(chunks, []byte(s[prev:k]))
		prev = k
	}
	chunks = append(chunks, []byte(s[prev:]))
	return chunks
}

// changedLines counts added and removed lines in a diff chunk, excluding
// the +++/--- file headers.
func changedLines(chunk []byte) int {
	n := 0
	for _, line := range strings.Split(string(chunk), "\n") {
		if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
			(strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
			n++
		}
	}
	return n
}

// hasGoFiles reports whether a diff chunk touches Go source files.
func hasGoFiles(chunk []byte) bool {
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "+++ b/") && strings.HasSuffix(strings.TrimSpace(line), ".go") {
			return true
		}
	}
	return false
}

// severityRank orders findings high → medium → low; advisory findings
// (empty severity) rank last — without the "" entry Go's zero value would
// tie them with high.
var severityRank = map[string]int{"high": 0, "medium": 1, "low": 2, "": 3}

// sortFindings orders findings high → medium → low → advisory, keeping the
// order within one severity stable.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		return severityRank[findings[i].Severity] < severityRank[findings[j].Severity]
	})
}

// line renders a finding in its canonical "[sev] location - message" form,
// used as the merge/dedup key across retry rounds.
func (f Finding) line() string {
	switch {
	case f.Severity == "":
		return "[?] " + f.Message
	case f.Location != "":
		return fmt.Sprintf("[%s] %s - %s", f.Severity, f.Location, f.Message)
	default:
		return fmt.Sprintf("[%s] %s", f.Severity, f.Message)
	}
}

// mergeFindings appends findings that are not already present (exact line
// match), preserving order.
func mergeFindings(existing, added []Finding) []Finding {
	seen := make(map[string]bool, len(existing))
	for _, f := range existing {
		seen[f.line()] = true
	}
	for _, f := range added {
		if !seen[f.line()] {
			seen[f.line()] = true
			existing = append(existing, f)
		}
	}
	return existing
}

// fixLinePrefixes are the suggestion-line markers a model may emit; the
// parser accepts both languages so a reply is understood regardless of the
// active output language.
var fixLinePrefixes = []string{"> 建议修改:", "> Suggested fix:"}

// splitFixLine strips a known fix-line prefix, returning the suggestion
// body and whether the line was a fix line at all.
func splitFixLine(line string) (body string, ok bool) {
	for _, p := range fixLinePrefixes {
		if strings.HasPrefix(line, p) {
			return strings.TrimSpace(strings.TrimPrefix(line, p)), true
		}
	}
	return "", false
}

// parseReview splits a model reply into findings and unparsable lines.
// A suggestion line is attached to a finding only when it directly
// follows that finding's line — any blank or other line in between resets the
// association, so a stray suggestion can never bind to the wrong finding.
func parseReview(raw string) (findings []Finding, bad []string) {
	s := cleanMessage(raw)
	if s == "" || strings.EqualFold(s, "OK") || meansNoIssues(s) && !strings.Contains(s, "[") {
		return nil, nil
	}
	lastIdx := -1
	prevWasFinding := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			prevWasFinding = false
			continue
		}
		if m := reviewFinding.FindStringSubmatch(line); m != nil {
			f := Finding{
				Severity: strings.ToLower(m[1]),
				Location: m[2],
				Message:  strings.TrimSpace(m[3]),
			}
			findings = append(findings, f)
			lastIdx = len(findings) - 1
			prevWasFinding = true
			continue
		}
		if prevWasFinding {
			if body, ok := splitFixLine(line); ok {
				findings[lastIdx].SuggestedFix = body
				prevWasFinding = false
				continue
			}
		}
		bad = append(bad, line)
		prevWasFinding = false
	}
	return findings, bad
}

// meansNoIssues recognizes replies like "无问题" or "no issues found" that
// some models emit instead of the bare OK token.
func meansNoIssues(s string) bool {
	if strings.Contains(s, "无问题") {
		return true
	}
	return strings.Contains(strings.ToLower(s), "no issues")
}

// truncateUTF8 cuts s to at most n bytes without splitting a multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// buildCommitSystemPrompt assembles the rules plus few-shot examples. The
// style is always Conventional Commits; the template and language rule
// adapt to the active language.
func buildCommitSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return fmt.Sprintf(`You are a git commit message generator. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except type keywords and proper nouns).
2. Output only the commit message itself: the subject on the first line, an optional body after a blank line. No explanations, no code fences, no quotes, no preamble or trailing remarks.
3. The subject follows Conventional Commits: type(scope): summary. type must be one of %s; the summary is at most %d characters, imperative, describing what was done.
4. Write a body only when the change exceeds %d lines or touches more than %d files: one short bullet per line, each starting with "- ", at most %d bullets. Small changes get no body.
5. When no type fits better, always use chore.

Example (4 files, ~180 changed lines, with body):
feat(auth): add token-based login endpoint

- add TokenLoginDTO and the login service
- let the filter pass token verification paths

Example (1 file, 8 changed lines, no body):
fix(cache): correct Redis connection pool timeout`,
			strings.Join(cfg.Types, "/"), cfg.SubjectMax,
			cfg.BodyMinLines, cfg.BodyMinFiles, cfg.BodyMaxItems)
	}
	return fmt.Sprintf(`你是 git commit message 生成器。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除 type 关键字与专有名词外,每个字都必须是中文)。
2. 只输出 commit message 本身:第一行是 subject,可选 body。不要解释、不要代码围栏、不要引号、不要任何前言或后缀。
3. subject 遵循 Conventional Commits:type(scope): 概要。type 只能取 %s;概要不超过 %d 字,用祈使句描述「做了什么」。
4. body 仅当改动超过 %d 行或涉及 %d 个以上文件时才编写;每条一行、以 "- " 开头的简短条目,最多 %d 条。小改动不要 body。
5. 没有更合适的 type 时一律用 chore。

示例(改动 4 个文件约 180 行,带 body):
feat(auth): 添加令牌登录接口

- 新增 TokenLoginDTO 与登录服务
- 过滤器放行令牌校验路径

示例(改动 1 个文件 8 行,不带 body):
fix(cache): 修正 Redis 连接池超时配置`,
		strings.Join(cfg.Types, "/"), cfg.SubjectMax,
		cfg.BodyMinLines, cfg.BodyMinFiles, cfg.BodyMaxItems)
}

func commitUserLead() string {
	if langIsEnglish() {
		return "Generate a commit message for the following staged diff:"
	}
	return "为以下暂存 diff 生成 commit message:"
}

func commitRetryLead() string {
	if langIsEnglish() {
		return "The previous output violated the rules: %s. Regenerate strictly following the rules; output only the commit message itself."
	}
	return "上次输出不合规:%s。请严格按规则重新生成,只输出 commit message 本身。"
}

var conventionalSubject = regexp.MustCompile(`^([a-z]+)(\([^)]*\))?: .+`)

// validateCommit returns "" when the message obeys the rules, otherwise a
// short reason (in the active language) suitable for feeding back to the
// model.
func validateCommit(msg string, cfg config.Commit) string {
	subject := strings.SplitN(msg, "\n", 2)[0]
	m := conventionalSubject.FindStringSubmatch(subject)
	if m == nil {
		if langIsEnglish() {
			return fmt.Sprintf("subject %q does not match the required type(scope): summary format", subject)
		}
		return fmt.Sprintf("subject %q 不符合 type(scope): 概要 的格式", subject)
	}
	if !contains(cfg.Types, m[1]) {
		if langIsEnglish() {
			return fmt.Sprintf("type %q is not in the allowed list (allowed: %s)", m[1], strings.Join(cfg.Types, ", "))
		}
		return fmt.Sprintf("type %q 不在允许列表中（允许：%s）", m[1], strings.Join(cfg.Types, ", "))
	}
	// The CJK check only guards Chinese output; English output is trusted.
	if !langIsEnglish() && !containsCJK(msg) {
		return "输出不是中文"
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// GeneratePRTitle asks the model for a one-line PR title for the given
// branch diff. It follows the same Conventional Commits style as commit
// messages but is allowed to be slightly broader (it describes the whole
// branch, not just the next commit).
func (c *Client) GeneratePRTitle(ctx context.Context, cfg config.Commit, diff []byte) (string, error) {
	body := string(diff)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(body) > cfg.MaxDiffChars {
		if c.Logf != nil {
			c.Logf("pr-title diff truncated: %d -> %d chars", len(body), cfg.MaxDiffChars)
		}
		body = truncateUTF8(body, cfg.MaxDiffChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}
	files, changedLines := diffStats(diff)
	messages := []chatMessage{
		{Role: "system", Content: prTitleSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\n%s%s",
			diffStatsLine(files, changedLines), prTitleUserLead(), body, truncated)},
	}
	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	msg := strings.SplitN(cleanMessage(raw), "\n", 2)[0]
	if msg == "" {
		return "", fmt.Errorf("%s", i18n.T("err_empty_pr_title"))
	}
	return msg, nil
}

func prTitleUserLead() string {
	if langIsEnglish() {
		return "Generate a one-line PR title for the following branch diff:"
	}
	return "请为以下分支 diff 生成一行 PR 标题:"
}

// GenerateExplanation asks the model to explain the content of a single file
// (or selected snippet) in plain language. The reply is cleaned but not
// rigidly validated.
func (c *Client) GenerateExplanation(ctx context.Context, cfg config.Commit, path string, content []byte) (string, error) {
	body := string(content)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(body) > cfg.MaxDiffChars {
		body = truncateUTF8(body, cfg.MaxDiffChars)
		truncated = "\n(Note: the content was truncated for length.)"
	}
	messages := []chatMessage{
		{Role: "system", Content: explainSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s%s", explainUserLead(path), body, truncated)},
	}
	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return cleanMessage(raw), nil
}

func explainUserLead(path string) string {
	if langIsEnglish() {
		return fmt.Sprintf("Explain the content and purpose of the following file (%s):", path)
	}
	return fmt.Sprintf("请解释以下文件 (%s) 的内容和作用:", path)
}

func prTitleSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return fmt.Sprintf(`You are a git PR title generator. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except type keywords and proper nouns).
2. Output only a single-line PR title: no explanations, no code fences, no quotes, no line breaks, no preamble or trailing remarks.
3. The title follows Conventional Commits style: type(scope): summary. type must be one of %s; keep the summary short and imperative, describing the overall purpose of the branch.
4. For a tiny branch you may omit the scope; when no type fits better, always use chore.`,
			strings.Join(cfg.Types, "/"))
	}
	return fmt.Sprintf(`你是 git PR 标题生成器。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除 type 关键字与专有名词外,每个字都必须是中文)。
2. 只输出一行 PR 标题,不要解释、不要代码围栏、不要引号、不要换行、不要任何前言或后缀。
3. 标题遵循 Conventional Commits 风格:type(scope): 概要。type 只能取 %s;概要简短,用祈使句描述本次分支的整体目的。
4. 如果分支改动很小,可以省略 scope;没有更合适 type 时一律用 chore。`,
		strings.Join(cfg.Types, "/"))
}

func prSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return fmt.Sprintf(`You are an experienced technical reviewer writing the PR title and description for a branch diff in a SINGLE reply. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except proper nouns and code identifiers).
2. The reply has exactly two parts: the title on the first line (a single line), then a blank line, then the markdown description body. No explanations, no code fence wrapping the whole output, no preamble or trailing remarks.
3. The title follows Conventional Commits style: type(scope): summary. type must be one of %s; keep the summary short and imperative, describing the overall purpose of the branch. For a tiny branch you may omit the scope; when no type fits better, always use chore.
4. The description body must contain exactly these two sections, each headed by a level-2 heading:
   ## Summary (2-4 sentences on background, motivation and impact)
   ## Main changes (bullets grouped by file or theme, each short and specific, starting with "- ")
5. For small changes the summary may be a single sentence, but keep the structure.`, strings.Join(cfg.Types, "/"))
	}
	return fmt.Sprintf(`你是经验丰富的技术 reviewer,为分支 diff 撰写 PR 标题与描述,一次性输出。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除专有名词与代码标识符外,每个字都必须是中文)。
2. 输出共两部分:第一行是 PR 标题(单行),空一行后是 markdown 描述正文。不要解释、不要代码围栏包裹整段输出、不要任何前言或后缀。
3. 标题遵循 Conventional Commits 风格:type(scope): 概要。type 只能取 %s;概要简短,用祈使句描述本次分支的整体目的。分支改动很小时可以省略 scope;没有更合适 type 时一律用 chore。
4. 描述正文必须恰好包含两个二级小节:
   ## 摘要(2-4 句话说明背景、动机和影响)
   ## 主要改动(按文件或主题列 bullet,每条简短具体,以 "- " 开头)
5. 如果改动很小,摘要可以只有 1 句话,但结构保持不变。`, strings.Join(cfg.Types, "/"))
}

func stashMsgSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return `You are a git stash message generator. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except proper nouns and code identifiers).
2. Output only one short line describing the theme of the current working-tree changes. No explanations, no code fences, no quotes, no line breaks, no preamble or trailing remarks.
3. Keep it under 80 characters, preferably starting with a verb such as feat/fix/refactor/docs/chore/test (no scope).
4. Examples: fix token validation in the login endpoint; add unit tests for the user service; refactor config loading logic`
	}
	return `你是 git stash message 生成器。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除专有名词与代码标识符外,每个字都必须是中文)。
2. 只输出一行简短描述,说明当前工作树改动的主题。不要解释、不要代码围栏、不要引号、不要换行、不要任何前言或后缀。
3. 长度控制在 80 字符以内,优先使用 "feat/fix/refactor/docs/chore/test" 等动词开头(不含 scope)。
4. 示例:修复登录接口 token 校验;添加用户服务单元测试;重构配置加载逻辑`
}

func explainSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return `You are a senior engineer explaining code to a colleague. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except proper nouns and code identifiers).
2. Summarize the core responsibility, key structure and caveats of the file or code snippet in 2-5 sentences.
3. Output only the explanation itself: no code fence wrapping the whole text, no preamble or trailing remarks.
4. If the code has obvious risks or anti-patterns, point them out too.`
	}
	return `你是资深工程师,向同事解释代码。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除专有名词与代码标识符外,每个字都必须是中文)。
2. 用 2-5 句话概括文件/代码段的核心职责、关键结构和注意事项。
3. 只输出解释本身,不要代码围栏包裹整段、不要任何前言或后缀。
4. 如果代码有明显风险或反模式,请一并指出。`
}

// GenerateSplitSuggestion asks the model to group a diff into logical commits.
// It returns a markdown description of the proposed groups.
func (c *Client) GenerateSplitSuggestion(ctx context.Context, cfg config.Commit, diff []byte) (string, error) {
	body := string(diff)
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(body) > cfg.MaxDiffChars {
		body = truncateUTF8(body, cfg.MaxDiffChars)
		truncated = "\n(Note: the diff was truncated for length.)"
	}
	files, changedLines := diffStats(diff)
	messages := []chatMessage{
		{Role: "system", Content: splitSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s\n\n%s%s",
			diffStatsLine(files, changedLines), splitUserLead(), body, truncated)},
	}
	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return cleanMessage(raw), nil
}

func splitUserLead() string {
	if langIsEnglish() {
		return "Suggest how to split the following diff into logical, independent commits:"
	}
	return "请将以下 diff 拆分为若干个逻辑独立的 commit,给出分组建议:"
}

func splitSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return `You are a git commit-splitting advisor. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except proper nouns and code identifiers).
2. Output only the markdown grouping suggestion: no preamble or trailing remarks.
3. One title per commit (Conventional Commits: type(scope): summary), followed by the list of files that commit should contain.
4. Do not split interdependent changes into different commits; do not split files that changed only because of the same bug.
5. If the current change is already a single logical commit, say no split is needed.`
	}
	return `你是 git commit 拆分顾问。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除专有名词与代码标识符外,每个字都必须是中文)。
2. 只输出 markdown 分组建议,不要任何前言或后缀。
3. 每个 commit 一行标题(遵循 Conventional Commits:type(scope): 概要),下面列出该 commit 应包含的文件列表。
4. 不要把相互依赖的改动拆到不同 commit;不要把仅因同一 bug 而改的不同文件拆开。
5. 如果当前改动已经是一个逻辑 commit,直接说明无需拆分。`
}

// GenerateChangelog asks the model for a markdown changelog from the provided
// git log/diff summary. The caller builds the input from git log and tags.
func (c *Client) GenerateChangelog(ctx context.Context, cfg config.Commit, input string) (string, error) {
	truncated := ""
	if cfg.MaxDiffChars > 0 && len(input) > cfg.MaxDiffChars {
		input = truncateUTF8(input, cfg.MaxDiffChars)
		truncated = "\n(Note: the input was truncated for length.)"
	}
	messages := []chatMessage{
		{Role: "system", Content: changelogSystemPrompt(cfg)},
		{Role: "user", Content: fmt.Sprintf("%s\n\n%s%s", changelogUserLead(), input, truncated)},
	}
	raw, err := c.chat(ctx, messages)
	if err != nil {
		return "", err
	}
	return cleanMessage(raw), nil
}

func changelogUserLead() string {
	if langIsEnglish() {
		return "Generate a changelog for the following commit history:"
	}
	return "请为以下提交历史生成 changelog:"
}

func changelogSystemPrompt(cfg config.Commit) string {
	if langIsEnglish() {
		return `You are a changelog writer. Obey these rules strictly:
1. The output language must be English (hard requirement: every word must be English except proper nouns and code identifiers).
2. Output only the markdown changelog itself: no preamble or trailing remarks.
3. Group entries by category (e.g. Features/Bug Fixes/Refactoring/Docs), one entry per line starting with "- ".
4. Merge semantically duplicate commits and drop chore/ci/style entries with no user value.
5. If the commit messages are too sparse, simply write "General maintenance and refactoring."`
	}
	return `你是 changelog 撰写者。严格遵守以下规则:
1. 输出语言必须是中文(硬性要求,除专有名词与代码标识符外,每个字都必须是中文)。
2. 只输出 markdown 格式的 changelog 本身,不要任何前言或后缀。
3. 按功能分类(如 Features/Bug Fixes/Refactoring/Docs),每条一行,以 "- " 开头。
4. 合并语义重复的提交,去除 chore/ci/style 等无用户价值的条目。
5. 如果提交信息不足,可以简单写 "常规维护与重构"。`
}

// diffStats counts touched files and changed lines (additions + deletions)
// in a unified diff, driving the body/no-body rule told to the model. Lines
// are only counted inside a hunk, so a removed line that starts with "--"
// is still a change, while ---/+++ file headers are not.
func diffStats(diff []byte) (files, changedLines int) {
	inHunk := false
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			files++
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
			// file header lines (index, ---, +++, mode, ...)
		case strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"):
			changedLines++
		}
	}
	return files, changedLines
}

// cleanMessage strips markdown fences, surrounding quotes and stray
// whitespace the model may add around the message.
func cleanMessage(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		// drop the opening fence line (``` or ```text) and the closing fence
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(s, "```"))
	}
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
