package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleOpenRouterCheck validates the configured API key without generating
// tokens. OpenRouter exposes GET /api/v1/key for the currently authenticated
// key, so this is cheaper and safer than using a completion as the primary
// connectivity check.
func (m *manager) handleOpenRouterCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	key, err := m.readOpenRouterKey()
	if err != nil {
		http.Error(w, "read OpenRouter API key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if key == "" {
		http.Error(w, "OpenRouter API key is not configured", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, openRouterBaseURL(c)+"/key", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	m.applyOpenRouterHeaders(req, key, c)

	started := time.Now()
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, "OpenRouter key check: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("OpenRouter key check HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), http.StatusBadGateway)
		return
	}

	var decoded struct {
		Data struct {
			Label          string `json:"label"`
			Usage          any    `json:"usage"`
			UsageDaily     any    `json:"usage_daily"`
			UsageWeekly    any    `json:"usage_weekly"`
			UsageMonthly   any    `json:"usage_monthly"`
			Limit          any    `json:"limit"`
			LimitRemaining any    `json:"limit_remaining"`
			IsFreeTier     bool   `json:"is_free_tier"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		http.Error(w, "OpenRouter key check returned invalid JSON", http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]any{
		"ok":              true,
		"label":           decoded.Data.Label,
		"usage":           decoded.Data.Usage,
		"usage_daily":     decoded.Data.UsageDaily,
		"usage_weekly":    decoded.Data.UsageWeekly,
		"usage_monthly":   decoded.Data.UsageMonthly,
		"limit":           decoded.Data.Limit,
		"limit_remaining": decoded.Data.LimitRemaining,
		"is_free_tier":    decoded.Data.IsFreeTier,
		"wall_ms":         time.Since(started).Milliseconds(),
	})
}
