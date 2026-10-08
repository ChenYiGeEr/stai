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
		if req.Temperature != 0 {
			t.Errorf("temperature should be 0, got %v", req.Temperature)
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

func TestValidateCommit(t *testing.T) {
	cases := []struct {
		name, msg, lang, wantReason string
	}{
		{"valid zh", "feat(core): 添加令牌登录", "zh-CN", ""},
		{"valid zh with body", "fix(cache): 修正超时\n\n- 调整连接池参数", "zh-CN", ""},
		{"valid en", "fix: repair pool timeout", "en", ""},
		{"no conventional format", "更新了登录逻辑", "zh-CN", "格式"},
		{"english when zh required", "feat: add login support", "zh-CN", "中文"},
		{"capitalized type", "Feat: 添加登录", "zh-CN", "格式"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := validateCommit(tc.msg, tc.lang)
			if tc.wantReason == "" && reason != "" {
				t.Errorf("unexpected rejection: %s", reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Errorf("reason %q should mention %q", reason, tc.wantReason)
			}
		})
	}
}

func TestGenerateCommitRetriesOnInvalid(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "This diff adds a new configuration feature" // invalid: prose, no conventional format
		if calls == 2 {
			content = "fix(cache): 修正连接池超时"
		}
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{{Message: chatMessage{Content: content}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	msg, err := c.GenerateCommit(context.Background(), CommitParams{
		Style: "conventional", Language: "zh-CN",
		Diff: []byte("diff --git a/x b/x\n+line"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg != "fix(cache): 修正连接池超时" {
		t.Errorf("expected retry result, got %q", msg)
	}
	if calls != 2 {
		t.Errorf("expected 2 provider calls, got %d", calls)
	}
}

func TestGenerateCommitNoRetryWhenValid(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{{Message: chatMessage{Content: "chore: 初始化"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model")
	if _, err := c.GenerateCommit(context.Background(), CommitParams{
		Style: "conventional", Language: "zh-CN",
		Diff: []byte("diff --git a/x b/x\n+line"), Retries: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("valid first reply must not be retried, got %d calls", calls)
	}
}

func TestDiffStats(t *testing.T) {
	diff := `diff --git a/x b/x
index 111..222 100644
--- a/x
+++ b/x
@@ -1,3 +1,4 @@
 keep
+add1
+add2
-del1
diff --git a/y b/y
--- a/y
+++ b/y
@@ -1 +1 @@
-old
+new
`
	files, lines := diffStats([]byte(diff))
	if files != 2 {
		t.Errorf("files = %d, want 2", files)
	}
	if lines != 5 { // add1 add2 del1 old new; ---/+++ excluded
		t.Errorf("changed lines = %d, want 5", lines)
	}
}
