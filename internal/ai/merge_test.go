package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseMergeAdvice(t *testing.T) {
	setLang(t, "zh-CN")
	explanation, choice, resolution, err := ParseMergeAdvice("EXPLANATION: 两边改了同一行\nCHOICE: merge\nRESOLUTION:\nmerged line\n")
	if err != nil {
		t.Fatal(err)
	}
	if explanation != "两边改了同一行" || choice != "merge" || resolution != "merged line\n" {
		t.Fatalf("got %q %q %q", explanation, choice, resolution)
	}

	_, choice, resolution, err = ParseMergeAdvice("```text\nEXPLANATION: keep local\nCHOICE: ours\nRESOLUTION:\nIGNORED\n```")
	if err != nil {
		t.Fatal(err)
	}
	if choice != "ours" || resolution != "" {
		t.Fatalf("ours must drop the resolution body, got %q %q", choice, resolution)
	}

	if _, _, _, err := ParseMergeAdvice("just keep ours"); err == nil {
		t.Fatal("unstructured output must be unusable")
	}
	if _, _, _, err := ParseMergeAdvice("EXPLANATION: bad\nCHOICE: merge\nRESOLUTION:\n<<<<<<< HEAD\nnope\n"); err == nil {
		t.Fatal("a resolution that contains conflict markers must be unusable")
	}
}

func TestSuggestMergeHunk(t *testing.T) {
	setLang(t, "en")
	var sawSchema bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		body := req.Messages[0].Content + "\n" + req.Messages[1].Content
		if strings.Contains(body, "EXPLANATION:") && strings.Contains(body, "OURS:") && strings.Contains(req.Messages[0].Content, "English") {
			sawSchema = true
		}
		json.NewEncoder(w).Encode(reply("EXPLANATION: keep theirs\nCHOICE: theirs\n"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "test-model", time.Minute)
	explanation, choice, resolution, err := c.SuggestMergeHunk(context.Background(), "base", "ours", "theirs")
	if err != nil {
		t.Fatal(err)
	}
	if !sawSchema {
		t.Fatal("prompt did not describe the schema and the two sides")
	}
	if explanation != "keep theirs" || choice != "theirs" || resolution != "" {
		t.Fatalf("got %q %q %q", explanation, choice, resolution)
	}
}

func TestSuggestMergeHunkProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", "test-model", time.Minute)
	_, _, _, err := c.SuggestMergeHunk(context.Background(), "", "ours", "theirs")
	if err == nil {
		t.Fatal("provider failure must surface as an error")
	}
}
