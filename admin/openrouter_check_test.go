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

func TestOpenRouterKeyCheckUsesNoGeneration(t *testing.T) {
	var method, path, auth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"label":"qnap-test","usage":1.25,"limit":10,"limit_remaining":8.75,"is_free_tier":false}}`)
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

	req := httptest.NewRequest(http.MethodGet, "/api/openrouter/check", nil)
	rr := httptest.NewRecorder()
	m.handleOpenRouterCheck(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if method != http.MethodGet || path != "/api/v1/key" {
		t.Fatalf("unexpected upstream request %s %s", method, path)
	}
	if !strings.HasPrefix(auth, "Bearer sk-or-") {
		t.Fatalf("authorization missing: %q", auth)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true || got["label"] != "qnap-test" || got["limit_remaining"] != 8.75 {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestOpenRouterDefaultsDoNotEmitProviderOverrides(t *testing.T) {
	cfg := defaults(config{})
	if cfg["OPENROUTER_ALLOW_FALLBACKS"] != "" || cfg["OPENROUTER_REQUIRE_PARAMETERS"] != "" {
		t.Fatalf("routing defaults must remain unset: %#v", cfg)
	}
	if got := openRouterProviderPreferences(cfg); len(got) != 0 {
		t.Fatalf("default config should not emit provider overrides: %#v", got)
	}
}

func TestSimplifiedOpenRouterUIKeepsAdvancedSettingsCollapsed(t *testing.T) {
	html := renderedIndexHTML()
	for _, want := range []string{
		"API接続確認（課金なし）",
		"すべてのテキストモデル",
		"詳細設定（通常は変更不要）",
		"/api/openrouter/check",
		"OpenRouter default (enabled)",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered UI missing %q", want)
		}
	}
	if strings.Count(html, `<div class="card"><h3>OpenRouter</h3>`) != 1 {
		t.Fatalf("OpenRouter card replacement failed")
	}
}
