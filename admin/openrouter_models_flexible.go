package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type openRouterCatalogModel struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	ContextLength       int             `json:"context_length"`
	Pricing             json.RawMessage `json:"pricing"`
	SupportedParameters []string        `json:"supported_parameters"`
	Architecture        struct {
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
}

type openRouterModelSummaryFlexible struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	ContextLength       int                 `json:"context_length,omitempty"`
	Pricing             map[string]string   `json:"pricing,omitempty"`
	PricingTiers        []map[string]string `json:"pricing_tiers,omitempty"`
	SupportedParameters []string            `json:"supported_parameters,omitempty"`
	Free                bool                `json:"free"`
	PolicyStatus        string              `json:"policy_status"`
	Blacklisted         bool                `json:"blacklisted"`
	BlacklistReason     string              `json:"blacklist_reason,omitempty"`
	SuccessCount        int                 `json:"success_count"`
	FailureCount        int                 `json:"failure_count"`
	CooldownUntil       string              `json:"cooldown_until,omitempty"`
}

func scalarOpenRouterPricing(raw map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for key, value := range raw {
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			out[key] = s
			continue
		}
		var n float64
		if err := json.Unmarshal(value, &n); err == nil {
			out[key] = strconv.FormatFloat(n, 'g', -1, 64)
		}
	}
	return out
}

func parseOpenRouterPricing(raw json.RawMessage) (map[string]string, []map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil, nil
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, nil, err
		}
		return scalarOpenRouterPricing(obj), nil, nil
	case '[':
		var rawTiers []map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rawTiers); err != nil {
			return nil, nil, err
		}
		tiers := make([]map[string]string, 0, len(rawTiers))
		for _, rawTier := range rawTiers {
			tiers = append(tiers, scalarOpenRouterPricing(rawTier))
		}
		if len(tiers) == 0 {
			return nil, tiers, nil
		}
		return tiers[0], tiers, nil
	default:
		return nil, nil, fmt.Errorf("unsupported pricing JSON type")
	}
}

var billablePriceKeys = []string{
	"prompt", "completion", "request", "image", "web_search",
	"internal_reasoning", "input_cache_read", "input_cache_write",
}

func pricingTierIsFree(pricing map[string]string) bool {
	seenCore := false
	for _, key := range billablePriceKeys {
		v := strings.TrimSpace(pricing[key])
		if v == "" {
			continue
		}
		if key == "prompt" || key == "completion" {
			seenCore = true
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n != 0 {
			return false
		}
	}
	return seenCore
}

func pricingIsFree(base map[string]string, tiers []map[string]string) bool {
	if len(tiers) == 0 {
		return pricingTierIsFree(base)
	}
	for _, tier := range tiers {
		if !pricingTierIsFree(tier) {
			return false
		}
	}
	return true
}

func openRouterCatalogItemPricing(item openRouterCatalogModel) (map[string]string, []map[string]string) {
	pricing, tiers, err := parseOpenRouterPricing(item.Pricing)
	if err != nil {
		return nil, nil
	}
	return pricing, tiers
}

func openRouterCatalogItemFree(item openRouterCatalogModel) bool {
	pricing, tiers := openRouterCatalogItemPricing(item)
	return strings.Contains(item.ID, ":free") || pricingIsFree(pricing, tiers)
}

func openRouterCatalogItemText(item openRouterCatalogModel) bool {
	if len(item.Architecture.OutputModalities) == 0 {
		// Backward compatibility with older/mock responses that omitted architecture.
		return true
	}
	for _, modality := range item.Architecture.OutputModalities {
		if strings.EqualFold(modality, "text") {
			return true
		}
	}
	return false
}

func (m *manager) fetchOpenRouterCatalog(ctx context.Context, c config, path string) ([]openRouterCatalogModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterBaseURL(c)+path, nil)
	if err != nil {
		return nil, err
	}
	if key, _ := m.readOpenRouterKey(); key != "" {
		m.applyOpenRouterHeaders(req, key, c)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("OpenRouter catalog HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []openRouterCatalogModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode OpenRouter models: %w", err)
	}
	return decoded.Data, nil
}

// /models/user is explicitly filtered by the authenticated user's provider
// preferences, privacy settings, and guardrails. It gives us a proactive policy
// compatibility signal without requiring a Management key.
func (m *manager) fetchOpenRouterUserCatalog(ctx context.Context, c config) ([]openRouterCatalogModel, error) {
	key, err := m.readOpenRouterKey()
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("OpenRouter API key is not configured")
	}
	return m.fetchOpenRouterCatalog(ctx, c, "/models/user")
}

func (m *manager) fetchOpenRouterUserModelIDs(ctx context.Context, c config) (map[string]bool, error) {
	models, err := m.fetchOpenRouterUserCatalog(ctx, c)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(models))
	for _, model := range models {
		if openRouterCatalogItemText(model) {
			ids[model.ID] = true
		}
	}
	return ids, nil
}

func (m *manager) fetchOpenRouterUserFreeModelIDs(ctx context.Context, c config) ([]string, error) {
	models, err := m.fetchOpenRouterUserCatalog(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(models))
	for _, model := range models {
		if openRouterCatalogItemText(model) && openRouterCatalogItemFree(model) {
			out = append(out, model.ID)
		}
	}
	return out, nil
}

func (m *manager) handleOpenRouterModelsFlexible(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	catalog, err := m.fetchOpenRouterCatalog(r.Context(), c, "/models?output_modalities=text")
	if err != nil {
		http.Error(w, "OpenRouter models: "+err.Error(), http.StatusBadGateway)
		return
	}

	policyKnown := false
	policyAllowed := map[string]bool{}
	if key, _ := m.readOpenRouterKey(); key != "" {
		if userCatalog, userErr := m.fetchOpenRouterUserCatalog(r.Context(), c); userErr == nil {
			policyKnown = true
			for _, item := range userCatalog {
				if openRouterCatalogItemText(item) {
					policyAllowed[item.ID] = true
				}
			}
		}
	}
	health := m.openRouterFallbackSnapshot()
	freeOnly := r.URL.Query().Get("free") == "1"
	out := make([]openRouterModelSummaryFlexible, 0, len(catalog))
	for _, item := range catalog {
		pricing, pricingTiers := openRouterCatalogItemPricing(item)
		free := strings.Contains(item.ID, ":free") || pricingIsFree(pricing, pricingTiers)
		if freeOnly && !free {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		policyStatus := "unknown"
		if policyKnown {
			if policyAllowed[item.ID] {
				policyStatus = "allowed"
			} else {
				policyStatus = "filtered"
			}
		}
		h := health[item.ID]
		out = append(out, openRouterModelSummaryFlexible{
			ID: item.ID, Name: name, ContextLength: item.ContextLength,
			Pricing: pricing, PricingTiers: pricingTiers,
			SupportedParameters: item.SupportedParameters, Free: free,
			PolicyStatus: policyStatus, Blacklisted: h.Blacklisted, BlacklistReason: h.BlacklistReason,
			SuccessCount: h.SuccessCount, FailureCount: h.FailureCount, CooldownUntil: h.CooldownUntil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		if out[i].PolicyStatus != out[j].PolicyStatus {
			return out[i].PolicyStatus == "allowed"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, map[string]any{
		"models": out, "count": len(out), "free_only": freeOnly,
		"policy_known": policyKnown,
		"policy_source": "OpenRouter /api/v1/models/user (provider preferences, privacy settings, guardrails)",
	})
}
