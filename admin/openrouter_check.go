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
// connectivity check. The same endpoint also returns per-key spend/limit
// metadata, which is safe to expose to the local admin UI without returning the
// secret key itself.
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
			Label               string `json:"label"`
			Usage               any    `json:"usage"`
			UsageDaily          any    `json:"usage_daily"`
			UsageWeekly         any    `json:"usage_weekly"`
			UsageMonthly        any    `json:"usage_monthly"`
			BYOKUsage           any    `json:"byok_usage"`
			BYOKUsageDaily      any    `json:"byok_usage_daily"`
			BYOKUsageWeekly     any    `json:"byok_usage_weekly"`
			BYOKUsageMonthly    any    `json:"byok_usage_monthly"`
			Limit               any    `json:"limit"`
			LimitRemaining      any    `json:"limit_remaining"`
			LimitReset          string `json:"limit_reset"`
			IncludeBYOKInLimit  bool   `json:"include_byok_in_limit"`
			IsFreeTier          bool   `json:"is_free_tier"`
			IsManagementKey     bool   `json:"is_management_key"`
			IsProvisioningKey   bool   `json:"is_provisioning_key"`
			ExpiresAt           any    `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		http.Error(w, "OpenRouter key check returned invalid JSON", http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]any{
		"ok":                    true,
		"label":                 decoded.Data.Label,
		"usage":                 decoded.Data.Usage,
		"usage_daily":           decoded.Data.UsageDaily,
		"usage_weekly":          decoded.Data.UsageWeekly,
		"usage_monthly":         decoded.Data.UsageMonthly,
		"byok_usage":            decoded.Data.BYOKUsage,
		"byok_usage_daily":      decoded.Data.BYOKUsageDaily,
		"byok_usage_weekly":     decoded.Data.BYOKUsageWeekly,
		"byok_usage_monthly":    decoded.Data.BYOKUsageMonthly,
		"limit":                 decoded.Data.Limit,
		"limit_remaining":       decoded.Data.LimitRemaining,
		"limit_reset":           decoded.Data.LimitReset,
		"include_byok_in_limit": decoded.Data.IncludeBYOKInLimit,
		"is_free_tier":          decoded.Data.IsFreeTier,
		"is_management_key":     decoded.Data.IsManagementKey,
		"is_provisioning_key":   decoded.Data.IsProvisioningKey,
		"expires_at":            decoded.Data.ExpiresAt,
		"wall_ms":               time.Since(started).Milliseconds(),
	})
}
