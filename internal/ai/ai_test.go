package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stai/internal/config"
)

// testCommit returns the default commit rules with the given language.
func testCommit(lang string) config.Commit {
	c := config.Default().Commit
	c.Language = lang
	return c
}

func reply(content string) chatResponse {
	return chatResponse{
		Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: content}}},
	}
}

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
		json.NewEncoder(w).Encode(reply("feat: hello"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	got, err := c.Chat(context.Background(), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if got != "feat: hello" {
		t.Errorf("got %q", got)
	}
}

func TestChatSendsIdentityHeaders(t *testing.T) {
	var ua, session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		session = r.Header.Get("x-opencode-session")
		json.NewEncoder(w).Encode(reply("fix: x"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	if _, err := c.Chat(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ua, "stai/") {
		t.Errorf("User-Agent = %q, want stai/...", ua)
	}
	if len(session) != 32 {
		t.Errorf("x-opencode-session = %q, want a 32-char run id", session)
	}
}

func TestChatErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	if _, err := c.Chat(context.Background(), "", ""); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestGenerateCommitCleansFences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reply("```\nfeat(core): 支持中文提交信息\n\n- 第一条\n```"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	msg, err := c.GenerateCommit(context.Background(), testCommit("zh-CN"), []byte("diff --git a/x b/x"))
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
		json.NewEncoder(w).Encode(reply("fix: x"))
	}))
	defer srv.Close()

	cfg := testCommit("en")
	cfg.MaxDiffChars = 100
	c := NewClient(srv.URL, "", "test-model", time.Minute)
	if _, err := c.GenerateCommit(context.Background(), cfg, []byte(strings.Repeat("x", 1000))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "truncated") {
		t.Errorf("model was not told the diff was truncated")
	}
	if strings.Contains(seen, strings.Repeat("x", 200)) {
		t.Errorf("diff not actually truncated: full 1000-char diff reached the model")
	}
}

func TestTruncateUTF8KeepsRunesWhole(t *testing.T) {
	s := "中文diff" // each Han rune is 3 bytes
	for n := 0; n <= len(s); n++ {
		got := truncateUTF8(s, n)
		if !strings.HasPrefix(s, got) {
			t.Fatalf("n=%d: %q is not a prefix", n, got)
		}
		if len(got) > n {
			t.Fatalf("n=%d: result %d bytes exceeds limit", n, len(got))
		}
	}
	if got := truncateUTF8(s, 4); got != "中" {
		t.Errorf("truncateUTF8(_, 4) = %q, want %q", got, "中")
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
		{"type not in list", "stai: 添加登录", "zh-CN", "允许列表"},
		{"type typo", "feet: 添加登录", "zh-CN", "允许列表"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := validateCommit(tc.msg, testCommit(tc.lang))
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
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	msg, err := c.GenerateCommit(context.Background(), testCommit("zh-CN"), []byte("diff --git a/x b/x\n+line"))
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

func TestRetryDoesNotResendDiff(t *testing.T) {
	var second chatRequest
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if calls == 2 {
			second = req
		}
		content := "bad output"
		if calls == 2 {
			content = "fix: ok"
		}
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	if _, err := c.GenerateCommit(context.Background(), testCommit("en"), []byte("diff --git a/x b/x\n+UNIQUE_DIFF_MARKER")); err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 4 {
		t.Fatalf("retry should continue the conversation (4 messages), got %d", len(second.Messages))
	}
	for i, m := range second.Messages {
		if i != 1 && strings.Contains(m.Content, "UNIQUE_DIFF_MARKER") {
			t.Errorf("message %d re-sends the diff on retry", i)
		}
	}
	if second.Messages[2].Role != "assistant" || second.Messages[3].Role != "user" {
		t.Errorf("unexpected roles: %s, %s", second.Messages[2].Role, second.Messages[3].Role)
	}
}

func TestGenerateCommitRejectsUnknownTypeAfterRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(reply("stai: 添加登录"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	msg, err := c.GenerateCommit(context.Background(), testCommit("zh-CN"), []byte("diff --git a/x b/x\n+line"))
	if err == nil {
		t.Fatalf("expected error for a still-invalid type, got message %q", msg)
	}
	if !strings.Contains(err.Error(), "允许列表") {
		t.Errorf("error should explain the rule, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 1 retry (2 calls), got %d", calls)
	}
}

func TestGenerateCommitNoRetryWhenValid(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(reply("chore: 初始化"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	if _, err := c.GenerateCommit(context.Background(), testCommit("zh-CN"), []byte("diff --git a/x b/x\n+line")); err != nil {
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

func TestDiffStatsCountsDashPrefixedContent(t *testing.T) {
	diff := `diff --git a/x b/x
--- a/x
+++ b/x
@@ -1,2 +1,1 @@
--- old separator
-// removed comment
+kept
`
	files, lines := diffStats([]byte(diff))
	if files != 1 {
		t.Errorf("files = %d, want 1", files)
	}
	if lines != 3 { // "-- old separator", "-// removed comment", "+kept"
		t.Errorf("changed lines = %d, want 3", lines)
	}
}
