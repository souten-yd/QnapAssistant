package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOpenRouterPricingSupportsTieredPricing(t *testing.T) {
	raw := json.RawMessage(`[
		{"prompt":"0.000002","completion":"0.000012","request":"0"},
		{"prompt":"0.000004","completion":"0.000018","min_context":200000}
	]`)
	base, tiers, err := parseOpenRouterPricing(raw)
	if err != nil {
		t.Fatal(err)
	}
	if base["prompt"] != "0.000002" || len(tiers) != 2 {
		t.Fatalf("unexpected pricing: base=%#v tiers=%#v", base, tiers)
	}
	if tiers[1]["min_context"] != "200000" || tiers[1]["completion"] != "0.000018" {
		t.Fatalf("long-context tier lost: %#v", tiers[1])
	}
	if pricingIsFree(base, tiers) {
		t.Fatal("paid tiered pricing must not be marked free")
	}
}

func TestParseOpenRouterPricingIgnoresUnknownStructuredValues(t *testing.T) {
	raw := json.RawMessage(`{
		"prompt":"0",
		"completion":"0",
		"request":"0",
		"future_complex_price":[{"unit":"thing","cost":1}]
	}`)
	base, tiers, err := parseOpenRouterPricing(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiers) != 0 || base["prompt"] != "0" {
		t.Fatalf("unexpected pricing: %#v %#v", base, tiers)
	}
	if _, ok := base["future_complex_price"]; ok {
		t.Fatalf("structured future field must be ignored: %#v", base)
	}
	if !pricingIsFree(base, tiers) {
		t.Fatalf("zero scalar pricing should be free: %#v", base)
	}
}

func TestOpenRouterModelsAcceptsObjectAndArrayPricing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" || r.URL.Query().Get("output_modalities") != "text" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"vendor/free","name":"Free","context_length":8192,"pricing":{"prompt":"0","completion":"0","request":"0","future":[1,2]},"supported_parameters":["tools"]},
			{"id":"vendor/tiered","name":"Tiered","context_length":300000,"pricing":[{"prompt":"0.000002","completion":"0.000012"},{"prompt":"0.000004","completion":"0.000018","min_context":200000}],"supported_parameters":[]}
		]}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	if err := os.WriteFile(m.configPath, []byte("OPENROUTER_BASE_URL="+upstream.URL+"/api/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openrouter/models?free=0", nil)
	m.handleOpenRouterModelsFlexible(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Models []openRouterModelSummaryFlexible `json:"models"`
		Count  int                              `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || len(got.Models) != 2 {
		t.Fatalf("unexpected catalog: %#v", got)
	}
	var freeModel, tiered *openRouterModelSummaryFlexible
	for i := range got.Models {
		switch got.Models[i].ID {
		case "vendor/free":
			freeModel = &got.Models[i]
		case "vendor/tiered":
			tiered = &got.Models[i]
		}
	}
	if freeModel == nil || !freeModel.Free {
		t.Fatalf("free model not recognized: %#v", freeModel)
	}
	if tiered == nil || tiered.Free || len(tiered.PricingTiers) != 2 {
		t.Fatalf("tiered model not preserved: %#v", tiered)
	}
}

func TestOpenRouterBillingUIIsInjected(t *testing.T) {
	html := injectOpenRouterBillingUI(injectUpdateUI(renderedIndexHTML()))
	for _, want := range []string{"id=\"orBilling\"", "APIキー使用実績", "pricing_tiers", "今月", "BYOK"} {
		if !strings.Contains(html, want) {
			t.Fatalf("billing UI missing %q", want)
		}
	}
}

func TestOpenRouterKeyCheckReturnsUsageWindows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"label":"qnap","usage":4.5,"usage_daily":0.2,"usage_weekly":1.1,"usage_monthly":3.4,"byok_usage":0.7,"byok_usage_monthly":0.6,"limit":10,"limit_remaining":5.5,"limit_reset":"monthly","is_free_tier":false,"is_management_key":false,"expires_at":"2027-12-31T23:59:59Z"}}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	m := &manager{configPath: filepath.Join(dir, "config.env")}
	if err := os.WriteFile(m.configPath, []byte("OPENROUTER_BASE_URL="+upstream.URL+"/api/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.writeOpenRouterKey("sk-or-test-only-not-a-real-secret-123456"); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	m.handleOpenRouterCheck(rr, httptest.NewRequest(http.MethodGet, "/api/openrouter/check", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["usage_daily"] != 0.2 || got["usage_monthly"] != 3.4 || got["byok_usage"] != 0.7 || got["limit_reset"] != "monthly" {
		t.Fatalf("billing fields lost: %#v", got)
	}
}
