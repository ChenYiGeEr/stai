package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("expected path /models, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
			t.Errorf("expected Bearer secret, got %q", auth)
		}
		_ = json.NewEncoder(w).Encode(modelsResponse{
			Data: []struct {
				ID string `json:"id"`
			}{
				{ID: "z-model"}, {ID: "a-model"}, {ID: "a-model"}, {ID: ""},
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "secret", "any", 0)
	got, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a-model", "z-model"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestListModelsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewClient(server.URL, "wrong", "any", 0)
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
