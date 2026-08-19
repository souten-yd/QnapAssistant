package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func rewrittenContent(t *testing.T, mode, model, content string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"Qwen3-0.6B","messages":[{"role":"user","content":`+mustJSON(content)+`}],"max_tokens":64}`))
	if err := applyThinkingMode(req, config{"THINKING_MODE": mode, "MODEL_PATH": model}); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	return messages[0].(map[string]any)["content"].(string)
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestApplyThinkingModeOff(t *testing.T) {
	got := rewrittenContent(t, "off", "/share/Public/Qwen3-0.6B-Q8_0.gguf", "こんにちは")
	if got != "こんにちは /no_think" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyThinkingModeOn(t *testing.T) {
	got := rewrittenContent(t, "on", "/share/Public/Qwen3-0.6B-Q8_0.gguf", "考えて")
	if got != "考えて /think" {
		t.Fatalf("got %q", got)
	}
}

func TestExplicitDirectiveWins(t *testing.T) {
	got := rewrittenContent(t, "off", "/share/Public/Qwen3-0.6B-Q8_0.gguf", "考えて /think")
	if got != "考えて /think" {
		t.Fatalf("got %q", got)
	}
}

func TestNonQwenPassesThrough(t *testing.T) {
	got := rewrittenContent(t, "off", "/share/Public/other.gguf", "こんにちは")
	if got != "こんにちは" {
		t.Fatalf("got %q", got)
	}
}
