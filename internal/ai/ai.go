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
)

type Client struct {
	baseURL     string
	apiKey      string
	model       string
	http        *http.Client
	sessionID   string  // one per run, so providers can group a conversation
	Temperature float64 // request temperature; 0 = deterministic. Some models only accept 1 — set it via config.
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
		{Role: "user", Content: fmt.Sprintf("改动统计:%d 个文件,%d 行变更(增删合计)。\n\n为以下暂存 diff 生成 commit message:\n\n%s%s",
			files, changedLines, body, truncated)},
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
			chatMessage{Role: "user", Content: fmt.Sprintf("上次输出不合规:%s。请严格按规则重新生成,只输出 commit message 本身。", reason)},
		)
		raw, err = c.chat(ctx, messages)
		if err != nil {
			return "", err
		}
		msg = cleanMessage(raw)
		reason = validateCommit(msg, cfg)
	}
	if reason != "" {
		return "", fmt.Errorf("模型输出仍不合规：%s", reason)
	}
	return msg, nil
}

// Finding is one issue the model found in the staged diff.
type Finding struct {
	Severity string // "high", "medium", "low" — "" means an unparsed advisory line
	Location string // e.g. "internal/ai/ai.go:42"; may be empty
	Message  string
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
	Logf          func(format string, args ...any) // optional per-group progress log
}

var reviewFinding = regexp.MustCompile(`^\[(high|medium|low)\]\s*([^\s]+)\s*-\s*(.+)$`)

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
		prefix = fmt.Sprintf("(第 %d/%d 组) ", num, total)
	}
	messages := []chatMessage{
		{Role: "system", Content: reviewPrompt(p.Rules, hasGoFiles(group))},
		{Role: "user", Content: fmt.Sprintf("%s审查以下暂存 diff:\n\n%s%s", prefix, diff, truncated)},
	}

	raw, err := c.chat(ctx, messages)
	if err != nil {
		return nil, err
	}
	findings, bad := parseReview(raw)

	for attempt := 0; len(bad) > 0 && attempt < p.Retries; attempt++ {
		messages = append(messages,
			chatMessage{Role: "assistant", Content: raw},
			chatMessage{Role: "user", Content: fmt.Sprintf(
				"上次输出无法解析:%q。请完整重新输出全部问题(包括已正确输出的),每条一行,格式为 [high|medium|low] 文件路径:行号 - 问题描述;没有问题就只输出 OK。",
				strings.Join(bad, " / "))},
		)
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

// reviewPrompt assembles the system prompt: the base principles, a condensed
// Go checklist when the group touches Go files, and any project rules.
func reviewPrompt(rules []string, hasGo bool) string {
	var b strings.Builder
	b.WriteString(reviewSystemPrompt)
	if hasGo {
		b.WriteString("\n\n" + reviewGoChecklist)
	}
	if len(rules) > 0 {
		b.WriteString("\n\n项目附加规则(必须遵守):\n")
		for _, r := range rules {
			b.WriteString("- " + r + "\n")
		}
	}
	return b.String()
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
4. 每条问题一行,格式为 [high|medium|low] 文件路径:行号 - 问题描述。high 仅用于会导致缺陷、数据丢失或安全漏洞的问题。
5. 问题描述用中文,一行一条,简短具体,指出改什么。
6. 没有任何问题时只输出 OK,不要输出其他任何内容。
7. 不要解释、不要代码围栏、不要任何前言或后缀。

示例(有 2 条问题):
[high] internal/ai/ai.go:102 - err 被覆盖,底层的读取错误会丢失
[low] cmd/stai/main.go:40 - 超时常量重复定义了两次

示例(无问题):
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

// parseReview splits a model reply into findings and unparsable lines.
func parseReview(raw string) (findings []Finding, bad []string) {
	s := cleanMessage(raw)
	if s == "" || strings.EqualFold(s, "OK") || strings.Contains(s, "无问题") && !strings.Contains(s, "[") {
		return nil, nil
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := reviewFinding.FindStringSubmatch(line); m != nil {
			findings = append(findings, Finding{
				Severity: m[1],
				Location: m[2],
				Message:  strings.TrimSpace(m[3]),
			})
		} else {
			bad = append(bad, line)
		}
	}
	return findings, bad
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
// style is always Conventional Commits; the language rule adapts to the
// configured output language.
func buildCommitSystemPrompt(cfg config.Commit) string {
	langRule := "输出语言必须是中文(硬性要求,除 type 关键字与专有名词外,每个字都必须是中文)。"
	if !strings.HasPrefix(strings.ToLower(cfg.Language), "zh") {
		langRule = "The output language must be English."
	}
	return fmt.Sprintf(`你是 git commit message 生成器。严格遵守以下规则:
1. %s
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
		langRule, strings.Join(cfg.Types, "/"), cfg.SubjectMax,
		cfg.BodyMinLines, cfg.BodyMinFiles, cfg.BodyMaxItems)
}

var conventionalSubject = regexp.MustCompile(`^([a-z]+)(\([^)]*\))?: .+`)

// validateCommit returns "" when the message obeys the rules, otherwise a
// short Chinese reason suitable for feeding back to the model.
func validateCommit(msg string, cfg config.Commit) string {
	subject := strings.SplitN(msg, "\n", 2)[0]
	m := conventionalSubject.FindStringSubmatch(subject)
	if m == nil {
		return fmt.Sprintf("subject %q 不符合 type(scope): 概要 的格式", subject)
	}
	if !contains(cfg.Types, m[1]) {
		return fmt.Sprintf("type %q 不在允许列表中（允许：%s）", m[1], strings.Join(cfg.Types, ", "))
	}
	if strings.HasPrefix(strings.ToLower(cfg.Language), "zh") && !containsCJK(msg) {
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
