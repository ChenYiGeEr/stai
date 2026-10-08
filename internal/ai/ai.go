// Package ai talks to an OpenAI-compatible chat completions endpoint
// (Ollama, LM Studio, api gateways, ...). The provider is plain HTTP+JSON
// so stai ships with zero third-party dependencies.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
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
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0, // deterministic: same diff -> same message
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

// CommitParams carries everything GenerateCommit needs to know.
type CommitParams struct {
	Style    string // "conventional" or "free"
	Language string // e.g. "zh-CN"
	Diff     []byte
	MaxChars int // truncate the diff beyond this, telling the model
	Retries  int // validation retries (0 = accept the first reply as-is)
}

// GenerateCommit asks the model for a commit message for the given diff and
// normalizes the reply (models love wrapping output in code fences). If the
// reply violates the format/language rules, it is retried up to Retries
// times with the failure reason fed back; a still-invalid reply is returned
// anyway (a mediocre message beats none).
func (c *Client) GenerateCommit(ctx context.Context, p CommitParams) (string, error) {
	diff := string(p.Diff)
	truncated := ""
	if p.MaxChars > 0 && len(diff) > p.MaxChars {
		diff = diff[:p.MaxChars]
		truncated = "\n(Note: the diff was truncated for length.)"
	}

	files, changedLines := diffStats(p.Diff)
	system := buildCommitSystemPrompt(p.Language)
	user := fmt.Sprintf("改动统计:%d 个文件,%d 行变更(增删合计)。\n\n为以下暂存 diff 生成 commit message:\n\n%s%s",
		files, changedLines, diff, truncated)

	raw, err := c.Chat(ctx, system, user)
	if err != nil {
		return "", err
	}
	msg := cleanMessage(raw)

	for attempt := 0; attempt < p.Retries; attempt++ {
		reason := validateCommit(msg, p.Language)
		if reason == "" {
			break
		}
		feedback := fmt.Sprintf("%s\n\n上次输出不合规:%s。请严格按规则重新生成,只输出 commit message 本身。",
			user, reason)
		retry, err := c.Chat(ctx, system, feedback)
		if err != nil {
			break
		}
		msg = cleanMessage(retry)
	}
	return msg, nil
}

// buildCommitSystemPrompt assembles the rules plus few-shot examples. The
// style is always Conventional Commits; the language rule adapts to the
// configured output language.
func buildCommitSystemPrompt(language string) string {
	langRule := "输出语言必须是中文(硬性要求,除 type 关键字与专有名词外,每个字都必须是中文)。"
	if !strings.HasPrefix(strings.ToLower(language), "zh") {
		langRule = "The output language must be English."
	}
	return `你是 git commit message 生成器。严格遵守以下规则:
1. ` + langRule + `
2. 只输出 commit message 本身:第一行是 subject,可选 body。不要解释、不要代码围栏、不要引号、不要任何前言或后缀。
3. subject 遵循 Conventional Commits:type(scope): 概要。type 只能取 feat/fix/refactor/docs/chore/test/style/perf/build/ci;概要不超过 50 字,用祈使句描述「做了什么」。
4. body 仅当改动超过 100 行或涉及 3 个以上文件时才编写;每条一行、以 "- " 开头的简短条目,最多 5 条。小改动不要 body。
5. 没有更合适的 type 时一律用 chore。

示例(改动 4 个文件约 180 行,带 body):
feat(auth): 添加令牌登录接口

- 新增 TokenLoginDTO 与登录服务
- 过滤器放行令牌校验路径

示例(改动 1 个文件 8 行,不带 body):
fix(cache): 修正 Redis 连接池超时配置`
}

var conventionalSubject = regexp.MustCompile(`^[a-z]+(\([^)]*\))?: .+`)

// validateCommit returns "" when the message obeys the rules, otherwise a
// short Chinese reason suitable for feeding back to the model.
func validateCommit(msg, language string) string {
	subject := strings.SplitN(msg, "\n", 2)[0]
	if !conventionalSubject.MatchString(subject) {
		return fmt.Sprintf("subject %q 不符合 type(scope): 概要 的格式", subject)
	}
	if strings.HasPrefix(strings.ToLower(language), "zh") && !containsCJK(msg) {
		return "输出不是中文"
	}
	return ""
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
// in a unified diff, driving the body/no-body rule told to the model.
func diffStats(diff []byte) (files, changedLines int) {
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			files++
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// hunk headers, not changes
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
