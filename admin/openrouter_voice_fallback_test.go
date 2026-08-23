package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyOpenRouterRetryError(t *testing.T) {
	c := classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 429: temporarily rate-limited upstream"))
	if !c.Retry || c.Blacklist || c.Status != 429 || c.Cooldown <= 0 {
		t.Fatalf("unexpected 429 class: %#v", c)
	}
	c = classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 404: No endpoints available matching your guardrail restrictions and data policy"))
	if !c.Retry || !c.Blacklist || c.Status != 404 {
		t.Fatalf("guardrail 404 must blacklist: %#v", c)
	}
	c = classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 404: temporary endpoint unavailable"))
	if !c.Retry || c.Blacklist || c.Cooldown <= 0 {
		t.Fatalf("generic 404 should be temporary: %#v", c)
	}
	c = classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 400: bad request"))
	if c.Retry || c.Blacklist {
		t.Fatalf("400 must not retry: %#v", c)
	}
}

func TestOpenRouterModelsShowsUserPolicyEligibility(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"test/blocked:free","name":"Blocked","context_length":4096,"pricing":{"prompt":"0","completion":"0"}},{"id":"test/allowed:free","name":"Allowed","context_length":8192,"pricing":{"prompt":"0","completion":"0"}}]}`)
		case "/api/v1/models/user":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-or-") {
				t.Fatalf("models/user missing auth: %q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"test/allowed:free","name":"Allowed","context_length":8192,"pricing":{"prompt":"0","completion":"0"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	if err := os.WriteFile(m.configPath, []byte("OPENROUTER_BASE_URL="+upstream.URL+"/api/v1\nLLM_PROVIDER=openrouter\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	m.handleOpenRouterModelsFlexible(rr, httptest.NewRequest(http.MethodGet, "/api/openrouter/models?free=1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		PolicyKnown bool `json:"policy_known"`
		Models      []openRouterModelSummaryFlexible `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.PolicyKnown || len(got.Models) != 2 {
		t.Fatalf("unexpected policy response: %#v", got)
	}
	status := map[string]string{}
	for _, m := range got.Models {
		status[m.ID] = m.PolicyStatus
	}
	if status["test/allowed:free"] != "allowed" || status["test/blocked:free"] != "filtered" {
		t.Fatalf("unexpected statuses: %#v", status)
	}
}

func TestVoiceStreamRetries429ToPolicyCompatibleFreeModel(t *testing.T) {
	var attempted []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models/user":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"test/fallback:free","name":"Fallback","context_length":4096,"pricing":{"prompt":"0","completion":"0"}}]}`)
		case "/api/v1/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			model, _ := payload["model"].(string)
			attempted = append(attempted, model)
			if model == "test/primary:free" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"message":"temporarily rate-limited upstream"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"model\":\"test/fallback:free\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env"), lastUsed: time.Now()}
	cfg := defaults(config{
		"LLM_PROVIDER": "openrouter", "OPENROUTER_BASE_URL": upstream.URL + "/api/v1",
		"OPENROUTER_MODEL": "test/primary:free", "OPENROUTER_VOICE_AUTO_FALLBACK": "1",
		"OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS": "3",
	})
	if err := saveConfig(m.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
	profile := voiceClientProfile{Name: "generic", ChunkMinChars: 4, ChunkMaxChars: 32}
	chunks, result := m.streamVoiceLLMStandardWithFallback(context.Background(), &http.Client{}, cfg, profile, "hello", voiceChatControls{}, time.Now())
	var streamed strings.Builder
	for chunk := range chunks {
		streamed.WriteString(chunk)
	}
	res := <-result
	if res.Err != nil || res.Reply != "OK" || streamed.String() != "OK" {
		t.Fatalf("unexpected result reply=%q streamed=%q err=%v", res.Reply, streamed.String(), res.Err)
	}
	if len(attempted) != 2 || attempted[0] != "test/primary:free" || attempted[1] != "test/fallback:free" {
		t.Fatalf("unexpected attempts: %#v", attempted)
	}
	health := m.openRouterFallbackSnapshot()
	if health["test/primary:free"].FailureCount != 1 || health["test/primary:free"].Blacklisted {
		t.Fatalf("429 health incorrect: %#v", health["test/primary:free"])
	}
	if health["test/fallback:free"].SuccessCount != 1 {
		t.Fatalf("fallback success missing: %#v", health["test/fallback:free"])
	}
}

func TestGuardrailFailurePersistsAndCanBeCleared(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	class := classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 404: No endpoints available matching your guardrail restrictions and data policy"))
	m.recordOpenRouterModelFailure("test/blocked:free", class)
	h := m.openRouterFallbackSnapshot()["test/blocked:free"]
	if !h.Blacklisted || h.FailureCount != 1 || h.BlacklistReason == "" {
		t.Fatalf("blacklist not persisted: %#v", h)
	}
	if err := m.clearOpenRouterBlacklist("test/blocked:free"); err != nil {
		t.Fatal(err)
	}
	h = m.openRouterFallbackSnapshot()["test/blocked:free"]
	if h.Blacklisted || h.BlacklistReason != "" || h.FailureCount != 1 {
		t.Fatalf("clear should retain history but remove blacklist: %#v", h)
	}
}

func TestFallbackConfigAndUI(t *testing.T) {
	cfg := defaults(config{})
	if cfg["OPENROUTER_VOICE_AUTO_FALLBACK"] != "1" || cfg["OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS"] != "3" {
		t.Fatalf("unexpected fallback defaults: %#v", cfg)
	}
	bad := defaults(config{"OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS": "6"})
	if msg := validateProviderConfig(bad); !strings.Contains(msg, "1..5") {
		t.Fatalf("expected max-attempt validation, got %q", msg)
	}
	html := injectOpenRouterFallbackUI(injectOpenRouterBillingUI(renderedIndexHTML()))
	for _, want := range []string{"音声ストリーム 自動フォールバック", "OPENROUTER_VOICE_AUTO_FALLBACK", "ブラックリストを全解除", "Policy除外", "/api/openrouter/fallback"} {
		if !strings.Contains(html, want) {
			t.Fatalf("fallback UI missing %q", want)
		}
	}
}
