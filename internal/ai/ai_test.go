package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestParseReview(t *testing.T) {
	findings, bad := parseReview(`[high] internal/ai/ai.go:102 - err 被覆盖,读取错误会丢失
[low] cmd/stai/main.go:40 - 超时常量重复定义

OK 以外的内容不应出现这里也不会解析`)
	if len(findings) != 2 || len(bad) != 1 {
		t.Fatalf("findings=%d bad=%d, want 2/1", len(findings), len(bad))
	}
	if findings[0].Severity != "high" || findings[0].Location != "internal/ai/ai.go:102" {
		t.Errorf("first finding mis-parsed: %+v", findings[0])
	}
	if findings[1].Severity != "low" || findings[1].Location != "cmd/stai/main.go:40" {
		t.Errorf("second finding mis-parsed: %+v", findings[1])
	}
	if !HasHigh(findings) {
		t.Errorf("HasHigh must be true with a high finding")
	}
}

func TestParseReviewOK(t *testing.T) {
	for _, s := range []string{"OK", "ok", "OK\n", "无问题"} {
		findings, bad := parseReview(s)
		if len(findings) != 0 || len(bad) != 0 {
			t.Errorf("parseReview(%q) = %d findings, %d bad; want none", s, len(findings), len(bad))
		}
	}
}

func TestGenerateReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reply("[medium] cmd/stai/main.go:88 - 错误未检查"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: []byte("diff --git a/x b/x\n+line"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != "medium" {
		t.Fatalf("findings = %+v", findings)
	}
	if HasHigh(findings) {
		t.Errorf("HasHigh must be false without a high finding")
	}
}

func TestGenerateReviewRetriesThenAdvises(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "我看这个 diff 没什么问题,挺好的" // unparseable prose
		if calls == 2 {
			content = "[high] a/b.go:1 - 空指针解引用"
		}
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: []byte("diff --git a/x b/x\n+line"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 1 retry (2 calls), got %d", calls)
	}
	if !HasHigh(findings) {
		t.Errorf("high finding lost after retry: %+v", findings)
	}
}

func TestGenerateReviewUnparsedBecomesAdvisory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(reply("这段代码整体不错,但错误处理可以更细致一些"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: []byte("diff --git a/x b/x\n+line"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != "" || findings[0].Message == "" {
		t.Fatalf("unparsed reply must become one advisory finding, got %+v", findings)
	}
	if HasHigh(findings) {
		t.Errorf("an unparsed line must never count as high severity")
	}
}

func TestGroupDiff(t *testing.T) {
	// Three files, two changed lines each (added+deleted pairs).
	diff := []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n+one\n+two\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1,2 @@\n-old\n+new\n" +
		"diff --git a/c.txt b/c.txt\n--- a/c.txt\n+++ b/c.txt\n@@ -1 +1,2 @@\n+x\n+y\n")

	if g := groupDiff(diff, 0); len(g) != 1 {
		t.Errorf("maxLines=0: got %d groups, want 1", len(g))
	}
	if g := groupDiff(diff, 2); len(g) != 3 {
		t.Errorf("maxLines=2: got %d groups, want 3 (one file each)", len(g))
	}
	if g := groupDiff(diff, 4); len(g) != 2 {
		t.Errorf("maxLines=4: got %d groups, want 2", len(g))
	}
	if g := groupDiff(diff, 1000); len(g) != 1 {
		t.Errorf("maxLines=1000: got %d groups, want 1", len(g))
	}
	// File headers must not count as changed lines.
	if g := groupDiff(diff, 3); len(g) != 3 {
		t.Errorf("maxLines=3: got %d groups, want 3 (headers excluded)", len(g))
	}
}

func TestGroupDiffKeepsOversizedFileWhole(t *testing.T) {
	var big strings.Builder
	big.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&big, "+line%d\n", i)
	}
	groups := groupDiff([]byte(big.String()), 10)
	if len(groups) != 1 {
		t.Fatalf("single oversized file: got %d groups, want 1 (file stays whole)", len(groups))
	}
	if !bytes.Equal(groups[0], []byte(big.String())) {
		t.Errorf("single oversized file must not be truncated by grouping")
	}
}

func TestSplitDiffFilesHeadersKept(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n+x\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n+y\n"
	chunks := splitDiffFiles([]byte(diff))
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	for i, want := range []string{"a.go", "b.go"} {
		if !strings.HasPrefix(string(chunks[i]), "diff --git a/"+want) {
			t.Errorf("chunk %d does not start with its %s header: %q", i, want, chunks[i][:20])
		}
	}
}

func TestReviewPromptAssembly(t *testing.T) {
	p := reviewPrompt([]string{"规则一"}, true)
	if !strings.Contains(p, "Go 专项关注点") || !strings.Contains(p, "规则一") {
		t.Errorf("rules and Go checklist missing from prompt:\n%s", p)
	}
	if !strings.Contains(p, "精确优先于召回") {
		t.Errorf("precision-over-recall principle missing from base prompt")
	}
	p = reviewPrompt(nil, false)
	if strings.Contains(p, "Go 专项关注点") || strings.Contains(p, "项目附加规则") {
		t.Errorf("prompt must stay bare without Go files or rules:\n%s", p)
	}
}

func TestGenerateReviewGrouped(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "[low] a/a.go:1 - a 的小问题"
		if calls == 2 {
			content = "[high] b/b.go:2 - b 的高危问题"
		}
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	diff := []byte("diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -1 +1,2 @@\n+x\n+y\n" +
		"diff --git a/b/b.go b/b/b.go\n--- a/b/b.go\n+++ b/b/b.go\n@@ -1 +1,2 @@\n+z\n+w\n")
	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: diff, GroupMaxLines: 2, Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 group calls, got %d", calls)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0].Severity != "high" || findings[1].Severity != "low" {
		t.Errorf("findings must be sorted high first: %+v", findings)
	}
}

func TestHasGoFiles(t *testing.T) {
	if !hasGoFiles([]byte("diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n+x")) {
		t.Errorf(".go chunk not detected")
	}
	if hasGoFiles([]byte("diff --git a/x.py b/x.py\n--- a/x.py\n+++ b/x.py\n+x")) {
		t.Errorf("non-Go chunk misdetected")
	}
}

func TestGenerateReviewParallel(t *testing.T) {
	// Three groups, concurrency 2: groups must overlap, merge and stay sorted.
	var inFlight, peak int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&inFlight, 1)
		if cur := atomic.LoadInt64(&inFlight); cur > atomic.LoadInt64(&peak) {
			atomic.StoreInt64(&peak, cur)
		}
		time.Sleep(30 * time.Millisecond)
		json.NewEncoder(w).Encode(reply("[medium] x/x.go:1 - 小问题"))
		atomic.AddInt64(&inFlight, -1)
	}))
	defer srv.Close()

	diff := []byte("diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -1 +1,2 @@\n+x\n+y\n" +
		"diff --git a/b/b.go b/b/b.go\n--- a/b/b.go\n+++ b/b.go\n@@ -1 +1,2 @@\n+z\n+w\n" +
		"diff --git a/c/c.go b/c/c.go\n--- a/c/c.go\n+++ b/c/c.go\n@@ -1 +1,2 @@\n+v\n+u\n")
	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: diff, GroupMaxLines: 2, Concurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %+v", findings)
	}
	if peak != 2 {
		t.Errorf("peak in-flight = %d, want 2 (concurrency limit)", peak)
	}
}

func TestGenerateReviewParallelFastFail(t *testing.T) {
	// The group containing a/a.go fails; the other two must already be
	// in flight by then (the failing handler waits for their signal), so
	// cancellation has to abort them — deterministically.
	var aborted int64
	slowStarted := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("a/a.go")) {
			<-slowStarted
			<-slowStarted
			http.Error(w, "boom", 500)
			return
		}
		slowStarted <- struct{}{}
		<-r.Context().Done() // simulate a slow provider until the client aborts
		atomic.AddInt64(&aborted, 1)
	}))
	defer srv.Close()

	diff := []byte("diff --git a/a/a.go b/a/a.go\n--- a/a/a.go\n+++ b/a/a.go\n@@ -1 +1,2 @@\n+x\n+y\n" +
		"diff --git a/b/b.go b/b/b.go\n--- a/b/b.go\n+++ b/b.go\n@@ -1 +1,2 @@\n+z\n+w\n" +
		"diff --git a/c/c.go b/c/c.go\n--- a/c/c.go\n+++ b/c/c.go\n@@ -1 +1,2 @@\n+v\n+u\n")
	c := NewClient(srv.URL, "", "test-model", time.Minute)
	started := time.Now()
	_, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: diff, GroupMaxLines: 2, Concurrency: 3,
	})
	if err == nil {
		t.Fatal("expected error from the failing group")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want the provider 500 (not a context error)", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("fast-fail took %s, want < 5s (in-flight groups must abort)", elapsed)
	}
	// Server handlers observe the client abort asynchronously; poll.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&aborted) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("in-flight requests observed no cancellation (aborted=%d)", atomic.LoadInt64(&aborted))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestChatSendsConfiguredTemperature(t *testing.T) {
	var got float64 = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		got = req.Temperature
		json.NewEncoder(w).Encode(reply("OK"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	c.Temperature = 1 // e.g. a model that rejects anything but 1
	if _, err := c.Chat(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("request temperature = %v, want 1", got)
	}
}

func TestGenerateReviewRetryAccumulates(t *testing.T) {
	// Round 1: one valid finding + one unparsable line. Round 2: the model
	// answers the feedback with ONLY the corrected line (partial re-emit) —
	// round-1 findings must survive.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "[high] a/a.go:1 - 空指针解引用\n这段是模型啰嗦的散文,不是 finding"
		if calls == 2 {
			content = "[medium] b/b.go:2 - 错误未检查"
		}
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n+a"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("retry must keep round-1 findings, got %+v", findings)
	}
	if !HasHigh(findings) {
		t.Errorf("high finding from round 1 lost: %+v", findings)
	}
}

func TestGenerateReviewRetryDedupsFullReemit(t *testing.T) {
	// Round 2 re-emits everything including the round-1 finding — it must
	// not appear twice.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := "[high] a/a.go:1 - 空指针解引用\n散文不是 finding"
		if calls == 2 {
			content = "[high] a/a.go:1 - 空指针解引用\n[low] c/c.go:3 - 命名不佳"
		}
		json.NewEncoder(w).Encode(reply(content))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	findings, err := c.GenerateReview(context.Background(), ReviewParams{
		Diff: []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n+a"), Retries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("full re-emit must dedup, got %+v", findings)
	}
}

func TestSortFindingsAdvisoryLast(t *testing.T) {
	findings := []Finding{
		{Severity: "", Message: "advisory"},
		{Severity: "low"},
		{Severity: "high"},
	}
	sortFindings(findings)
	want := []string{"high", "low", ""}
	for i, w := range want {
		if findings[i].Severity != w {
			t.Fatalf("order = %q, want %q first", findings[i].Severity, w)
		}
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
