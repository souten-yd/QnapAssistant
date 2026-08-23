package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetOpenRouterFallbackCacheForTest() {
	openRouterFreeFallbackCache.Lock()
	defer openRouterFreeFallbackCache.Unlock()
	openRouterFreeFallbackCache.cacheKey = ""
	openRouterFreeFallbackCache.at = time.Time{}
	openRouterFreeFallbackCache.models = nil
}

func writeOpenRouterTestConfig(t *testing.T, m *manager, baseURL string, extra string) {
	t.Helper()
	content := "OPENROUTER_BASE_URL=" + baseURL + "\n" + extra
	if err := os.WriteFile(m.configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyAwareCatalogMarksModelsFilteredByPrivacyOrGuardrails(t *testing.T) {
	var userCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/models":
			_, _ = io.WriteString(w, `{"data":[
				{"id":"vendor/allowed:free","name":"Allowed","context_length":131072,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]},"top_provider":{"is_moderated":false}},
				{"id":"vendor/blocked:free","name":"Blocked","context_length":65536,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]},"top_provider":{"is_moderated":true}}
			]}`)
		case "/api/v1/models/user":
			userCalls++
			_, _ = io.WriteString(w, `{"data":[{"id":"vendor/allowed:free","name":"Allowed","context_length":131072,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	writeOpenRouterTestConfig(t, m, upstream.URL+"/api/v1", "")

	req := httptest.NewRequest(http.MethodGet, "/api/openrouter/models?free=1", nil)
	rr := httptest.NewRecorder()
	m.handleOpenRouterModelsPolicyAware(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if userCalls != 1 {
		t.Fatalf("expected one /models/user preflight, got %d", userCalls)
	}
	var got struct {
		PolicyChecked bool `json:"policy_checked"`
		Models []struct {
			ID            string `json:"id"`
			PolicyChecked bool   `json:"policy_checked"`
			PolicyAllowed bool   `json:"policy_allowed"`
			PolicyStatus  string `json:"policy_status"`
			Moderated     bool   `json:"moderated"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.PolicyChecked || len(got.Models) != 2 {
		t.Fatalf("unexpected response: %#v", got)
	}
	byID := map[string]struct {
		allowed bool
		status string
		moderated bool
	}{}
	for _, model := range got.Models {
		byID[model.ID] = struct {
			allowed bool
			status string
			moderated bool
		}{model.PolicyAllowed, model.PolicyStatus, model.Moderated}
	}
	if !byID["vendor/allowed:free"].allowed {
		t.Fatalf("allowed model was marked blocked: %#v", byID)
	}
	blocked := byID["vendor/blocked:free"]
	if blocked.allowed || blocked.status != "Privacy/Guardrail制限" || !blocked.moderated {
		t.Fatalf("blocked model metadata wrong: %#v", blocked)
	}
}

func TestAutomaticFreeFallbackUsesOnlyUserPolicyCatalogAndPrefersManualFree(t *testing.T) {
	resetOpenRouterFallbackCacheForTest()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/models/user" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"vendor/large:free","name":"Large","context_length":200000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]},"supported_parameters":["temperature"]},
			{"id":"vendor/preferred:free","name":"Preferred","context_length":100000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]},"supported_parameters":["temperature"]},
			{"id":"vendor/paid","name":"Paid","context_length":999999,"pricing":{"prompt":"0.000001","completion":"0.000001"},"architecture":{"output_modalities":["text"]}}
		]}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	writeOpenRouterTestConfig(t, m, upstream.URL+"/api/v1",
		"OPENROUTER_MODEL=vendor/primary:free\n"+
			"OPENROUTER_FALLBACK_MODELS=vendor/paid,vendor/preferred:free\n"+
			"OPENROUTER_AUTO_FREE_FALLBACK=1\n"+
			"OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS=3\n")
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = defaults(cfg)
	payload := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}}
	applyOpenRouterPayload(cfg, payload)
	m.applyOpenRouterAutoFreeFallback(context.Background(), cfg, payload)

	models, ok := payload["models"].([]string)
	if !ok {
		t.Fatalf("expected models array, got %#v", payload)
	}
	want := []string{"vendor/primary:free", "vendor/preferred:free", "vendor/large:free"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("models=%v want=%v", models, want)
	}
	for _, model := range models {
		if model == "vendor/paid" {
			t.Fatalf("paid model leaked into free fallback chain: %v", models)
		}
	}
}

func TestAutomaticFreeFallbackAttemptCountIsClamped(t *testing.T) {
	cfg := defaults(config{"OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS": "99"})
	if got := openRouterAutoFreeFallbackAttempts(cfg); got != 5 {
		t.Fatalf("got %d want 5", got)
	}
	cfg["OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS"] = "0"
	if got := openRouterAutoFreeFallbackAttempts(cfg); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestFallbackConfigValidation(t *testing.T) {
	cfg := defaults(config{})
	cfg["OPENROUTER_AUTO_FREE_FALLBACK"] = "1"
	cfg["OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS"] = "5"
	if msg := validateProviderConfig(cfg); msg != "" {
		t.Fatalf("valid config rejected: %s", msg)
	}
	cfg["OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS"] = "6"
	if msg := validateProviderConfig(cfg); !strings.Contains(msg, "between 1 and 5") {
		t.Fatalf("invalid attempts not rejected: %q", msg)
	}
}

func TestFallbackUIShowsPolicyAndAttemptControls(t *testing.T) {
	html := injectOpenRouterFallbackUI(injectOpenRouterBillingUI(renderedIndexHTML()))
	for _, want := range []string{
		"無料モデル自動フォールバック",
		"OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS",
		"Privacy/Guardrail制限",
		"429と入力内容依存Guardrailは事前判定不可",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered UI missing %q", want)
		}
	}
}
