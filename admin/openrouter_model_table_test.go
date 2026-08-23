package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPolicyResetClearsOnlyPolicyBlacklistAndKeepsUsage(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}

	policy := classifyOpenRouterVoiceFailure(http.StatusNotFound, "No endpoints available matching your guardrail restrictions and data policy")
	m.recordOpenRouterVoiceFailure("vendor/policy:free", policy)
	m.recordOpenRouterVoiceSuccess("vendor/policy:free")
	rate := classifyOpenRouterVoiceFailure(http.StatusTooManyRequests, "temporarily rate-limited upstream")
	m.recordOpenRouterVoiceFailure("vendor/rate:free", rate)

	openRouterFreeFallbackCache.Lock()
	openRouterFreeFallbackCache.cacheKey = "stale-policy-cache"
	openRouterFreeFallbackCache.at = time.Now()
	openRouterFreeFallbackCache.models = []openRouterFreeFallbackCandidate{{ID: "vendor/policy:free"}}
	openRouterFreeFallbackCache.Unlock()

	if err := m.resetOpenRouterVoicePolicyState(); err != nil {
		t.Fatal(err)
	}
	h := m.openRouterVoiceHealthSnapshot()
	policyHealth := h["vendor/policy:free"]
	if policyHealth.Blacklisted || policyHealth.BlacklistReason != "" {
		t.Fatalf("policy blacklist not cleared: %#v", policyHealth)
	}
	if policyHealth.SuccessCount != 1 || policyHealth.FailureCount != 1 {
		t.Fatalf("usage counters should survive policy reset: %#v", policyHealth)
	}
	rateHealth := h["vendor/rate:free"]
	if rateHealth.CooldownUntil == "" {
		t.Fatalf("rate-limit cooldown should survive policy reset: %#v", rateHealth)
	}
	openRouterFreeFallbackCache.Lock()
	defer openRouterFreeFallbackCache.Unlock()
	if openRouterFreeFallbackCache.cacheKey != "" || !openRouterFreeFallbackCache.at.IsZero() || len(openRouterFreeFallbackCache.models) != 0 {
		t.Fatalf("policy reset did not invalidate /models/user cache")
	}
}

func TestResetVoiceHistoryClearsUsageAndBlacklist(t *testing.T) {
	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	m.recordOpenRouterVoiceSuccess("vendor/a:free")
	m.recordOpenRouterVoiceFailure("vendor/a:free", classifyOpenRouterVoiceFailure(http.StatusNotFound, "guardrail restrictions and data policy"))
	if len(m.openRouterVoiceHealthSnapshot()) == 0 {
		t.Fatal("test setup failed")
	}
	if err := m.resetOpenRouterVoiceHistory(); err != nil {
		t.Fatal(err)
	}
	if got := len(m.openRouterVoiceHealthSnapshot()); got != 0 {
		t.Fatalf("history still has %d entries", got)
	}
}

func TestPolicyCatalogExposesCreatedAndUsageMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"vendor/new:free","name":"New","created":1786034890,"context_length":131072,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}}]}`)
		case "/api/v1/models/user":
			_, _ = io.WriteString(w, `{"data":[{"id":"vendor/new:free","name":"New","created":1786034890,"context_length":131072,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	writeOpenRouterTestConfig(t, m, upstream.URL+"/api/v1", "")
	m.recordOpenRouterVoiceSuccess("vendor/new:free")
	m.recordOpenRouterVoiceFailure("vendor/new:free", openRouterVoiceFailureClass{Status: 500, Reason: "test"})

	rr := httptest.NewRecorder()
	m.handleOpenRouterModelsPolicyAware(rr, httptest.NewRequest(http.MethodGet, "/api/openrouter/models?free=0", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Models []openRouterPolicyModelSummary `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 {
		t.Fatalf("models=%d", len(got.Models))
	}
	model := got.Models[0]
	if model.Created != 1786034890 || model.VoiceUseCount != 2 || model.VoiceSuccessCount != 1 || model.VoiceFailureCount != 1 {
		t.Fatalf("unexpected metadata: %#v", model)
	}
}

func TestOpenRouterModelTableUIHasSortFilterAndPagination(t *testing.T) {
	html := renderedIndexHTML()
	html = injectOpenRouterBillingUI(html)
	html = injectOpenRouterFallbackUI(html)
	html = injectOpenRouterVoiceHealthUI(html)
	html = injectOpenRouterModelTableUI(html)
	for _, want := range []string{
		`id="orModelTableBody"`,
		"状態フィルタ",
		"料金フィルタ",
		"利用回数",
		"最終利用",
		"追加日",
		"入出力合計単価",
		`value="10"`,
		`value="20"`,
		"Policy再評価",
		"利用実績をリセット",
		"reset_policy_state",
		"reset_history",
		"localStorage",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("model table UI missing %q", want)
		}
	}
	if strings.Count(html, `id="orModelManager"`) != 1 {
		t.Fatalf("model manager duplicated: %d", strings.Count(html, `id="orModelManager"`))
	}
}

func TestPolicyFailureRecognitionIsNarrow(t *testing.T) {
	for _, msg := range []string{"guardrail restrictions", "data policy", "privacy settings", "zero data retention", "ZDR required"} {
		if !openRouterVoicePolicyFailureReason(msg) {
			t.Fatalf("expected policy signal for %q", msg)
		}
	}
	for _, msg := range []string{"No endpoints available", "temporarily unavailable", fmt.Sprintf("HTTP %d", http.StatusNotFound)} {
		if openRouterVoicePolicyFailureReason(msg) {
			t.Fatalf("unexpected policy signal for %q", msg)
		}
	}
}
