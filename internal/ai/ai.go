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
	"strings"
	"time"
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
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
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
		Stream: false,
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
}

// GenerateCommit asks the model for a commit message for the given diff and
// normalizes the reply (models love wrapping output in code fences).
func (c *Client) GenerateCommit(ctx context.Context, p CommitParams) (string, error) {
	diff := string(p.Diff)
	truncated := ""
	if p.MaxChars > 0 && len(diff) > p.MaxChars {
		diff = diff[:p.MaxChars]
		truncated = "\n(Note: the diff was truncated for length.)"
	}

	system := fmt.Sprintf(`You write git commit messages.
Rules:
- Output ONLY the commit message itself: one subject line, optionally a short body. No explanations, no code fences, no quotation marks, no preamble.
- Follow the %s convention for the subject line.
- Write the message in %s.`,
		styleDescription(p.Style), p.Language)
	user := "Generate a commit message for the following staged diff:\n\n" + diff + truncated

	raw, err := c.Chat(ctx, system, user)
	if err != nil {
		return "", err
	}
	return cleanMessage(raw), nil
}

func styleDescription(style string) string {
	if style == "free" {
		return "free-form (imperative mood, concise)"
	}
	return "Conventional Commits (type(scope): subject — types like feat, fix, refactor, docs, chore, test)"
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
