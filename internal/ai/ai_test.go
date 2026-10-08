package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{{Message: chatMessage{Role: "assistant", Content: "feat: hello"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	got, err := c.Chat(context.Background(), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if got != "feat: hello" {
		t.Errorf("got %q", got)
	}
}

func TestChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	if _, err := c.Chat(context.Background(), "", ""); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestGenerateCommitCleansFences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{{Message: chatMessage{Content: "```\nfeat(core): 支持中文提交信息\n\n- 第一条\n```"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	msg, err := c.GenerateCommit(context.Background(), CommitParams{
		Style:    "conventional",
		Language: "zh-CN",
		Diff:     []byte("diff --git a/x b/x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "feat(core): 支持中文提交信息\n\n- 第一条"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestGenerateCommitTruncates(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		seen = req.Messages[1].Content
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{{Message: chatMessage{Content: "fix: x"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	if _, err := c.GenerateCommit(context.Background(), CommitParams{
		Style: "conventional", Language: "en",
		Diff: []byte(strings.Repeat("x", 1000)), MaxChars: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "truncated") {
		t.Errorf("model was not told the diff was truncated")
	}
	if strings.Contains(seen, strings.Repeat("x", 200)) {
		t.Errorf("diff not actually truncated: full 1000-char diff reached the model")
	}
}
