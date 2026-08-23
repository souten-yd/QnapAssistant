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

func TestClassifyOpenRouterVoiceFailure(t *testing.T) {
	c := classifyOpenRouterVoiceFailure(429, "temporarily rate-limited upstream")
	if !c.Retry || c.Blacklist || c.Cooldown <= 0 {
		t.Fatalf("unexpected 429 classification: %#v", c)
	}
	c = classifyOpenRouterVoiceFailure(404, "No endpoints available matching your guardrail restrictions and data policy")
	if !c.Retry || !c.Blacklist {
		t.Fatalf("policy 404 must blacklist: %#v", c)
	}
	c = classifyOpenRouterVoiceFailure(404, "No endpoints available matching current routing")
	if !c.Retry || c.Blacklist || c.Cooldown <= 0 {
		t.Fatalf("generic 404 must be temporary: %#v", c)
	}
	c = classifyOpenRouterVoiceFailure(400, "bad request")
	if c.Retry || c.Blacklist {
		t.Fatalf("400 must not retry: %#v", c)
	}
}

func TestOpenRouterVoiceHealthPersistsAndBlacklistClears(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	class := classifyOpenRouterVoiceFailure(404, "No endpoints available matching your guardrail restrictions and data policy")
	m.recordOpenRouterVoiceFailure("test/blocked:free", class)
	h := m.openRouterVoiceHealthSnapshot()["test/blocked:free"]
	if !h.Blacklisted || h.FailureCount != 1 || h.BlacklistReason == "" {
		t.Fatalf("blacklist not persisted: %#v", h)
	}
	if err := m.clearOpenRouterVoiceBlacklist("test/blocked:free"); err != nil {
		t.Fatal(err)
	}
	h = m.openRouterVoiceHealthSnapshot()["test/blocked:free"]
	if h.Blacklisted || h.BlacklistReason != "" || h.FailureCount != 1 {
		t.Fatalf("clear should preserve history: %#v", h)
	}
}

func TestVoiceStreamRetries429ToPolicyCompatibleFreeModel(t *testing.T) {
	var attempted []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models/user":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"test/fallback:free","name":"Fallback","context_length":8192,"architecture":{"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}}]}`)
		case "/api/v1/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			model, _ := payload["model"].(string)
			attempted = append(attempted, model)
			if model == "test/primary:free" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"message":"test/primary:free is temporarily rate-limited upstream"}}`)
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
		"OPENROUTER_MODEL": "test/primary:free", "OPENROUTER_AUTO_FREE_FALLBACK": "1",
		"OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS": "3",
	})
	if err := saveConfig(m.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
	profile := voiceClientProfile{Name: "generic", ChunkMinChars: 1, ChunkMaxChars: 32}
	chunks, result := m.streamVoiceLLMStandard(context.Background(), &http.Client{}, cfg, profile, "hello", voiceChatControls{}, time.Now())
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
	health := m.openRouterVoiceHealthSnapshot()
	if health["test/primary:free"].FailureCount != 1 || health["test/primary:free"].Blacklisted {
		t.Fatalf("429 should be temporary: %#v", health["test/primary:free"])
	}
	if health["test/fallback:free"].SuccessCount != 1 {
		t.Fatalf("fallback success missing: %#v", health["test/fallback:free"])
	}
}

func TestVoiceStreamDoesNotRetryAfterTextWasEmitted(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/chat/completions":
			requests++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"error\":{\"code\":429,\"message\":\"temporarily rate-limited upstream\"}}\n\n")
		case "/api/v1/models/user":
			_, _ = io.WriteString(w, `{"data":[{"id":"test/other:free","architecture":{"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env"), lastUsed: time.Now()}
	cfg := defaults(config{
		"LLM_PROVIDER": "openrouter", "OPENROUTER_BASE_URL": upstream.URL + "/api/v1",
		"OPENROUTER_MODEL": "test/primary:free", "OPENROUTER_AUTO_FREE_FALLBACK": "1",
		"OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS": "3",
	})
	if err := saveConfig(m.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
	profile := voiceClientProfile{Name: "generic", ChunkMinChars: 1, ChunkMaxChars: 32}
	chunks, result := m.streamVoiceLLMStandard(context.Background(), &http.Client{}, cfg, profile, "hello", voiceChatControls{}, time.Now())
	var got []string
	for chunk := range chunks {
		got = append(got, chunk)
	}
	res := <-result
	if res.Err == nil || !strings.Contains(res.Err.Error(), "HTTP 429") {
		t.Fatalf("expected terminal 429 after partial output, got %v", res.Err)
	}
	if requests != 1 || strings.Join(got, "") != "A" {
		t.Fatalf("must not switch model after output: requests=%d chunks=%#v", requests, got)
	}
}

func TestVoiceRetryHealthScorePrefersSuccessfulHistory(t *testing.T) {
	good := openRouterVoiceHealthScore(openRouterVoiceModelHealth{SuccessCount: 8, FailureCount: 1})
	unknown := openRouterVoiceHealthScore(openRouterVoiceModelHealth{})
	bad := openRouterVoiceHealthScore(openRouterVoiceModelHealth{SuccessCount: 1, FailureCount: 8})
	if !(good > unknown && unknown > bad) {
		t.Fatalf("unexpected scores good=%v unknown=%v bad=%v", good, unknown, bad)
	}
}

func TestOpenRouterVoiceHealthAPIReturnsSafeState(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	m.recordOpenRouterVoiceSuccess("test/ok:free")
	rr := httptest.NewRecorder()
	m.handleOpenRouterVoiceHealth(rr, httptest.NewRequest(http.MethodGet, "/api/openrouter/voice-health", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "test/ok:free") || strings.Contains(rr.Body.String(), "sk-or-") {
		t.Fatalf("unexpected health API response: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPolicyFailureMessageMatchesUserObservedError(t *testing.T) {
	err := fmt.Errorf("LLM stream request failed: HTTP 404: No endpoints available matching your guardrail restrictions and data policy. Configure privacy settings")
	class := classifyOpenRouterVoiceError(err)
	if !class.Retry || !class.Blacklist || class.Status != 404 {
		t.Fatalf("user-observed policy failure must blacklist: %#v", class)
	}
}
