package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

func boolConfig(c config, key string, d bool) bool {
	v, ok := c[key]
	if !ok || strings.TrimSpace(v) == "" {
		return d
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return d
	}
}

func normalizedLLMProvider(c config) string {
	switch strings.ToLower(strings.TrimSpace(get(c, "LLM_PROVIDER", "local"))) {
	case "openrouter", "open-router", "remote":
		return "openrouter"
	default:
		return "local"
	}
}

func llmAutoUnload(c config) bool {
	if _, ok := c["LLM_AUTO_UNLOAD"]; ok {
		return boolConfig(c, "LLM_AUTO_UNLOAD", false)
	}
	return !keepModelsLoaded(c)
}

func llmIdleTimeout(c config) int {
	if strings.TrimSpace(c["LLM_IDLE_TIMEOUT_SECONDS"]) != "" {
		return intVal(c, "LLM_IDLE_TIMEOUT_SECONDS", 300)
	}
	return intVal(c, "IDLE_TIMEOUT_SECONDS", 300)
}

func openRouterBaseURL(c config) string {
	return strings.TrimRight(get(c, "OPENROUTER_BASE_URL", defaultOpenRouterBaseURL), "/")
}

func (m *manager) openRouterKeyPath() string {
	return filepath.Join(filepath.Dir(m.configPath), "openrouter.key")
}

func (m *manager) readOpenRouterKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); v != "" {
		return v, nil
	}
	b, err := os.ReadFile(m.openRouterKeyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (m *manager) writeOpenRouterKey(key string) error {
	key = strings.TrimSpace(key)
	path := m.openRouterKeyPath()
	if key == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if !strings.HasPrefix(key, "sk-or-") || len(key) < 24 {
		return fmt.Errorf("OpenRouter API key must start with sk-or-")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func openRouterKeyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])[:12]
}

func (m *manager) handleOpenRouterKey(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		key, err := m.readOpenRouterKey()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"configured": key != "", "fingerprint": openRouterKeyFingerprint(key), "storage": "0600 secret file or OPENROUTER_API_KEY environment"})
	case http.MethodPut:
		var req struct {
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := m.writeOpenRouterKey(req.APIKey); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key, _ := m.readOpenRouterKey()
		writeJSON(w, map[string]any{"ok": true, "configured": key != "", "fingerprint": openRouterKeyFingerprint(key)})
	case http.MethodDelete:
		if err := m.writeOpenRouterKey(""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "configured": false})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func splitCSV(value string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(value, ",") {
		v := strings.TrimSpace(raw)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func openRouterModelChain(c config) []string {
	primary := strings.TrimSpace(get(c, "OPENROUTER_MODEL", "openrouter/free"))
	out := []string{}
	if primary != "" {
		out = append(out, primary)
	}
	for _, model := range splitCSV(c["OPENROUTER_FALLBACK_MODELS"]) {
		duplicate := false
		for _, existing := range out {
			if existing == model {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, model)
		}
	}
	if len(out) == 0 {
		out = []string{"openrouter/free"}
	}
	return out
}

func optionalConfigFloat(c config, key string) (float64, bool) {
	raw := strings.TrimSpace(c[key])
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func openRouterProviderPreferences(c config) map[string]any {
	p := map[string]any{}
	if v := strings.TrimSpace(c["OPENROUTER_PROVIDER_SORT"]); v == "price" || v == "throughput" || v == "latency" {
		p["sort"] = v
	}
	if strings.TrimSpace(c["OPENROUTER_ALLOW_FALLBACKS"]) != "" {
		p["allow_fallbacks"] = boolConfig(c, "OPENROUTER_ALLOW_FALLBACKS", true)
	}
	if strings.TrimSpace(c["OPENROUTER_REQUIRE_PARAMETERS"]) != "" {
		p["require_parameters"] = boolConfig(c, "OPENROUTER_REQUIRE_PARAMETERS", false)
	}
	if v := strings.TrimSpace(c["OPENROUTER_DATA_COLLECTION"]); v == "allow" || v == "deny" {
		p["data_collection"] = v
	}
	if strings.TrimSpace(c["OPENROUTER_ZDR"]) != "" {
		p["zdr"] = boolConfig(c, "OPENROUTER_ZDR", false)
	}
	if values := splitCSV(c["OPENROUTER_PROVIDER_ONLY"]); len(values) > 0 {
		p["only"] = values
	}
	if values := splitCSV(c["OPENROUTER_PROVIDER_IGNORE"]); len(values) > 0 {
		p["ignore"] = values
	}
	maxPrice := map[string]float64{}
	if v, ok := optionalConfigFloat(c, "OPENROUTER_MAX_PRICE_PROMPT"); ok && v >= 0 {
		maxPrice["prompt"] = v
	}
	if v, ok := optionalConfigFloat(c, "OPENROUTER_MAX_PRICE_COMPLETION"); ok && v >= 0 {
		maxPrice["completion"] = v
	}
	if len(maxPrice) > 0 {
		p["max_price"] = maxPrice
	}
	return p
}

func applyOpenRouterPayload(c config, payload map[string]any) {
	models := openRouterModelChain(c)
	if len(models) > 1 {
		delete(payload, "model")
		payload["models"] = models
	} else {
		delete(payload, "models")
		payload["model"] = models[0]
	}
	if v := strings.TrimSpace(c["OPENROUTER_PRESET"]); v != "" {
		payload["preset"] = v
	}
	if v, ok := optionalConfigFloat(c, "OPENROUTER_TEMPERATURE"); ok && v >= 0 && v <= 2 {
		payload["temperature"] = v
	}
	if v, ok := optionalConfigFloat(c, "OPENROUTER_TOP_P"); ok && v >= 0 && v <= 1 {
		payload["top_p"] = v
	}
	if v := strings.TrimSpace(c["OPENROUTER_REASONING_EFFORT"]); v != "" && v != "default" {
		payload["reasoning_effort"] = v
	}
	if provider := openRouterProviderPreferences(c); len(provider) > 0 {
		payload["provider"] = provider
	}
	delete(payload, "chat_template_kwargs")
	if payload["reasoning_effort"] == "none" && strings.TrimSpace(c["OPENROUTER_REASONING_EFFORT"]) == "" {
		delete(payload, "reasoning_effort")
	}
}

func (m *manager) prepareLLMRequest(ctx context.Context, c config, payload map[string]any) (*http.Request, error) {
	provider := normalizedLLMProvider(c)
	if provider == "openrouter" {
		key, err := m.readOpenRouterKey()
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, fmt.Errorf("OpenRouter API key is not configured")
		}
		applyOpenRouterPayload(c, payload)
		m.applyOpenRouterAutoFreeFallback(ctx, c, payload)
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterBaseURL(c)+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		m.applyOpenRouterHeaders(req, key, c)
		return req, nil
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+c["BACKEND_PORT"]+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (m *manager) applyOpenRouterHeaders(req *http.Request, key string, c config) {
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if v := strings.TrimSpace(c["OPENROUTER_HTTP_REFERER"]); v != "" {
		req.Header.Set("HTTP-Referer", v)
	}
	if v := strings.TrimSpace(get(c, "OPENROUTER_X_TITLE", "QnapAssistant")); v != "" {
		req.Header.Set("X-Title", v)
	}
}

func (m *manager) openRouterReady() error {
	key, err := m.readOpenRouterKey()
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("OpenRouter API key is not configured")
	}
	if !strings.HasPrefix(key, "sk-or-") {
		return fmt.Errorf("configured OpenRouter API key has an invalid prefix")
	}
	return nil
}

func (m *manager) handleOpenRouterProxy(w http.ResponseWriter, r *http.Request, c config) {
	key, err := m.readOpenRouterKey()
	if err != nil || key == "" {
		http.Error(w, "OpenRouter API key is not configured", http.StatusServiceUnavailable)
		return
	}
	base, err := url.Parse(openRouterBaseURL(c))
	if err != nil {
		http.Error(w, "invalid OpenRouter base URL", http.StatusInternalServerError)
		return
	}
	target := &url.URL{Scheme: base.Scheme, Host: base.Host}
	p := httputil.NewSingleHostReverseProxy(target)
	originalDirector := p.Director
	p.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = strings.TrimRight(base.Path, "/") + strings.TrimPrefix(r.URL.Path, "/v1")
		req.Host = base.Host
		m.applyOpenRouterHeaders(req, key, c)
	}
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "OpenRouter proxy: "+err.Error(), http.StatusBadGateway)
	}
	p.ServeHTTP(w, r)
}

type openRouterModelSummary struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	ContextLength       int               `json:"context_length,omitempty"`
	Pricing             map[string]string `json:"pricing,omitempty"`
	SupportedParameters []string          `json:"supported_parameters,omitempty"`
	Free                bool              `json:"free"`
}

func zeroPrice(v string) bool {
	if strings.TrimSpace(v) == "" {
		return false
	}
	n, err := strconv.ParseFloat(v, 64)
	return err == nil && n == 0
}

func (m *manager) handleOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, openRouterBaseURL(c)+"/models?output_modalities=text", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if key, _ := m.readOpenRouterKey(); key != "" {
		m.applyOpenRouterHeaders(req, key, c)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "OpenRouter models: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		http.Error(w, fmt.Sprintf("OpenRouter models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), http.StatusBadGateway)
		return
	}
	var decoded struct {
		Data []struct {
			ID                  string            `json:"id"`
			Name                string            `json:"name"`
			ContextLength       int               `json:"context_length"`
			Pricing             map[string]string `json:"pricing"`
			SupportedParameters []string          `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&decoded); err != nil {
		http.Error(w, "decode OpenRouter models: "+err.Error(), http.StatusBadGateway)
		return
	}
	freeOnly := r.URL.Query().Get("free") == "1"
	out := make([]openRouterModelSummary, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		free := strings.Contains(item.ID, ":free") || (zeroPrice(item.Pricing["prompt"]) && zeroPrice(item.Pricing["completion"]))
		if freeOnly && !free {
			continue
		}
		out = append(out, openRouterModelSummary{ID: item.ID, Name: item.Name, ContextLength: item.ContextLength, Pricing: item.Pricing, SupportedParameters: item.SupportedParameters, Free: free})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, map[string]any{"models": out, "count": len(out), "free_only": freeOnly})
}

func (m *manager) handleOpenRouterTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Model     string `json:"model"`
		Message   string `json:"message"`
		MaxTokens int    `json:"max_tokens"`
		AllowPaid bool   `json:"allow_paid"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in)
	if in.Message == "" {
		in.Message = "Reply with only: OK"
	}
	if in.MaxTokens <= 0 || in.MaxTokens > 32 {
		in.MaxTokens = 8
	}
	if strings.TrimSpace(in.Model) == "" || !in.AllowPaid {
		in.Model = "openrouter/free"
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	key, err := m.readOpenRouterKey()
	if err != nil || key == "" {
		http.Error(w, "OpenRouter API key is not configured", http.StatusBadRequest)
		return
	}
	payload := map[string]any{
		"model": in.Model, "messages": []map[string]string{{"role": "user", "content": in.Message}},
		"max_tokens": in.MaxTokens, "temperature": 0,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, openRouterBaseURL(c)+"/chat/completions", bytes.NewReader(body))
	m.applyOpenRouterHeaders(req, key, c)
	started := time.Now()
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, "OpenRouter test: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("OpenRouter test HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), http.StatusBadGateway)
		return
	}
	var decoded struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Choices  []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
		Usage any `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		http.Error(w, "OpenRouter returned invalid JSON", http.StatusBadGateway)
		return
	}
	content, finish := "", ""
	if len(decoded.Choices) > 0 {
		content = decoded.Choices[0].Message.Content
		finish = decoded.Choices[0].FinishReason
	}
	writeJSON(w, map[string]any{"ok": true, "model": decoded.Model, "provider": decoded.Provider, "finish_reason": finish, "content": content, "usage": decoded.Usage, "wall_ms": time.Since(started).Milliseconds(), "paid_test_allowed": in.AllowPaid})
}
