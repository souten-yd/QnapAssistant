package main

import (
	"bytes"
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
}

type openRouterModelSummaryFlexible struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	ContextLength       int                 `json:"context_length,omitempty"`
	Pricing             map[string]string   `json:"pricing,omitempty"`
	PricingTiers        []map[string]string `json:"pricing_tiers,omitempty"`
	SupportedParameters []string            `json:"supported_parameters,omitempty"`
	Free                bool                `json:"free"`
}

// scalarOpenRouterPricing keeps scalar pricing values while tolerating future
// structured values. OpenRouter pricing values are usually strings, but parsing
// numbers too makes the catalog resilient to harmless schema representation
// changes.
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

// parseOpenRouterPricing accepts both documented pricing forms:
//   {"prompt":"...","completion":"..."}
// and tiered pricing:
//   [{...base...},{...long-context...,"min_context":200000}]
// The first tier is returned as Pricing for backward-compatible clients while
// all tiers are exposed separately for richer UI display.
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
		pricing := scalarOpenRouterPricing(obj)
		return pricing, nil, nil
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

func (m *manager) handleOpenRouterModelsFlexible(w http.ResponseWriter, r *http.Request) {
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
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
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
		Data []openRouterCatalogModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&decoded); err != nil {
		http.Error(w, "decode OpenRouter models: "+err.Error(), http.StatusBadGateway)
		return
	}

	freeOnly := r.URL.Query().Get("free") == "1"
	out := make([]openRouterModelSummaryFlexible, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		pricing, pricingTiers, err := parseOpenRouterPricing(item.Pricing)
		if err != nil {
			// One unusual model must not break the whole catalog. Preserve the
			// model and omit pricing rather than returning a 502 for every model.
			pricing, pricingTiers = nil, nil
		}
		free := strings.Contains(item.ID, ":free") || pricingIsFree(pricing, pricingTiers)
		if freeOnly && !free {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		out = append(out, openRouterModelSummaryFlexible{
			ID: item.ID, Name: name, ContextLength: item.ContextLength,
			Pricing: pricing, PricingTiers: pricingTiers,
			SupportedParameters: item.SupportedParameters, Free: free,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, map[string]any{"models": out, "count": len(out), "free_only": freeOnly})
}
