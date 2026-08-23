package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenRouterModelChainAndProviderPreferences(t *testing.T) {
	cfg := defaults(config{
		"OPENROUTER_MODEL":                "model/a",
		"OPENROUTER_FALLBACK_MODELS":      "model/b, model/a, model/c",
		"OPENROUTER_PROVIDER_SORT":         "latency",
		"OPENROUTER_ALLOW_FALLBACKS":       "1",
		"OPENROUTER_REQUIRE_PARAMETERS":    "1",
		"OPENROUTER_DATA_COLLECTION":       "deny",
		"OPENROUTER_ZDR":                   "1",
		"OPENROUTER_PROVIDER_ONLY":         "p1,p2",
		"OPENROUTER_PROVIDER_IGNORE":       "p3",
		"OPENROUTER_MAX_PRICE_PROMPT":      "0.5",
		"OPENROUTER_MAX_PRICE_COMPLETION":  "1.5",
	})
	if got, want := openRouterModelChain(cfg), []string{"model/a", "model/b", "model/c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model chain=%v want %v", got, want)
	}
	p := openRouterProviderPreferences(cfg)
	if p["sort"] != "latency" || p["data_collection"] != "deny" || p["zdr"] != true {
		t.Fatalf("provider preferences incomplete: %#v", p)
	}
	if !reflect.DeepEqual(p["only"], []string{"p1", "p2"}) || !reflect.DeepEqual(p["ignore"], []string{"p3"}) {
		t.Fatalf("provider allow/ignore=%#v", p)
	}
}

func TestApplyOpenRouterPayloadOverridesLocalSpecificFields(t *testing.T) {
	cfg := defaults(config{
		"LLM_PROVIDER":                "openrouter",
		"OPENROUTER_MODEL":            "model/a",
		"OPENROUTER_FALLBACK_MODELS":  "model/b",
		"OPENROUTER_TEMPERATURE":       "0.4",
		"OPENROUTER_TOP_P":             "0.8",
		"OPENROUTER_REASONING_EFFORT":  "low",
	})
	payload := map[string]any{
		"model": "local.gguf",
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"reasoning_effort": "none",
	}
	applyOpenRouterPayload(cfg, payload)
	if _, ok := payload["chat_template_kwargs"]; ok {
		t.Fatal("llama.cpp chat_template_kwargs leaked to OpenRouter")
	}
	models, ok := payload["models"].([]string)
	if !ok || !reflect.DeepEqual(models, []string{"model/a", "model/b"}) {
		t.Fatalf("models=%#v", payload["models"])
	}
	if _, ok := payload["model"]; ok {
		t.Fatal("model should be omitted when an ordered fallback chain is used")
	}
	if payload["reasoning_effort"] != "low" || payload["temperature"] != 0.4 || payload["top_p"] != 0.8 {
		t.Fatalf("OpenRouter sampling/reasoning not applied: %#v", payload)
	}
}

func TestOpenRouterSecretStoredSeparatelyWith0600(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	key := "sk-or-test-only-not-a-real-secret-123456"
	if err := m.writeOpenRouterKey(key); err != nil {
		t.Fatal(err)
	}
	got, err := m.readOpenRouterKey()
	if err != nil || got != key {
		t.Fatalf("read key=%q err=%v", got, err)
	}
	st, err := os.Stat(filepath.Join(dir, "openrouter.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("secret mode=%#o want 0600", st.Mode().Perm())
	}
	cfg := defaults(config{"LLM_PROVIDER": "openrouter"})
	if err := saveConfig(m.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(m.configPath)
	if strings.Contains(string(b), key) || strings.Contains(string(b), "API_KEY=") {
		t.Fatal("API key leaked into persistent non-secret config")
	}
}

func TestOpenRouterSafeTestForcesFreeRouter(t *testing.T) {
	var seenModel string
	var seenMax float64
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		seenAuth = r.Header.Get("Authorization")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		seenModel, _ = payload["model"].(string)
		seenMax, _ = payload["max_tokens"].(float64)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"free/model","provider":"test","choices":[{"finish_reason":"stop","message":{"content":"OK"}}],"usage":{"prompt_tokens":4,"completion_tokens":1}}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	cfg := defaults(config{"OPENROUTER_BASE_URL": upstream.URL + "/api/v1"})
	if err := saveConfig(m.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/openrouter/test", strings.NewReader(`{"model":"paid/model","max_tokens":999}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	m.handleOpenRouterTest(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if seenModel != "openrouter/free" {
		t.Fatalf("unsafe model=%q; safe test must force openrouter/free", seenModel)
	}
	if seenMax != 8 {
		t.Fatalf("max_tokens=%v want 8", seenMax)
	}
	if !strings.HasPrefix(seenAuth, "Bearer sk-or-") {
		t.Fatalf("authorization missing: %q", seenAuth)
	}
}

func TestLegacyResidencyMigratesWithoutBehaviorChange(t *testing.T) {
	cfg := defaults(config{"KEEP_MODELS_LOADED": "0", "IDLE_TIMEOUT_SECONDS": "123"})
	if cfg["LLM_AUTO_UNLOAD"] != "1" || cfg["LLM_IDLE_TIMEOUT_SECONDS"] != "123" {
		t.Fatalf("legacy migration failed: %#v", cfg)
	}
}
