package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type openRouterPolicyCatalogModel struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	ContextLength int             `json:"context_length"`
	Pricing       json.RawMessage `json:"pricing"`
	Architecture  struct {
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	TopProvider struct {
		IsModerated bool `json:"is_moderated"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
}

type openRouterPolicyModelSummary struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	ContextLength       int                 `json:"context_length,omitempty"`
	Pricing             map[string]string   `json:"pricing,omitempty"`
	PricingTiers        []map[string]string `json:"pricing_tiers,omitempty"`
	SupportedParameters []string            `json:"supported_parameters,omitempty"`
	Free                bool                `json:"free"`
	PolicyChecked       bool                `json:"policy_checked"`
	PolicyAllowed       bool                `json:"policy_allowed"`
	PolicyStatus        string              `json:"policy_status"`
	Moderated           bool                `json:"moderated"`
}

func openRouterModelOutputsText(m openRouterPolicyCatalogModel) bool {
	if len(m.Architecture.OutputModalities) == 0 {
		// Older / variant catalog records may omit architecture. The caller is
		// already using the text model endpoint/filter, so keep them.
		return true
	}
	for _, modality := range m.Architecture.OutputModalities {
		if modality == "text" {
			return true
		}
	}
	return false
}

func (m *manager) fetchOpenRouterPolicyCatalog(ctx context.Context, c config, path string, key string) ([]openRouterPolicyCatalogModel, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, openRouterBaseURL(c)+path, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		m.applyOpenRouterHeaders(req, key, c)
	}
	resp, err := (&http.Client{Timeout: 9 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
		return nil, fmt.Errorf("OpenRouter catalog HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []openRouterPolicyCatalogModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 12<<20)).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Data, nil
}

func openRouterPolicyCatalogIDs(models []openRouterPolicyCatalogModel) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			out[id] = true
		}
	}
	return out
}

func (m *manager) handleOpenRouterModelsPolicyAware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	key, _ := m.readOpenRouterKey()

	// /models is the full public catalog. /models/user is the same model
	// surface filtered by this API key's provider preferences, privacy settings,
	// and guardrails. Comparing them gives a cheap preflight signal without a
	// billable completion request.
	allModels, err := m.fetchOpenRouterPolicyCatalog(r.Context(), c, "/models?output_modalities=text", key)
	if err != nil {
		http.Error(w, "OpenRouter models: "+err.Error(), http.StatusBadGateway)
		return
	}
	policyChecked := false
	allowedIDs := map[string]bool{}
	policyError := ""
	if key != "" {
		userModels, userErr := m.fetchOpenRouterPolicyCatalog(r.Context(), c, "/models/user", key)
		if userErr == nil {
			policyChecked = true
			allowedIDs = openRouterPolicyCatalogIDs(userModels)
		} else {
			policyError = userErr.Error()
		}
	}

	freeOnly := r.URL.Query().Get("free") == "1"
	out := make([]openRouterPolicyModelSummary, 0, len(allModels))
	for _, item := range allModels {
		if !openRouterModelOutputsText(item) {
			continue
		}
		pricing, tiers, pricingErr := parseOpenRouterPricing(item.Pricing)
		if pricingErr != nil {
			pricing, tiers = nil, nil
		}
		free := strings.Contains(item.ID, ":free") || pricingIsFree(pricing, tiers)
		if freeOnly && !free {
			continue
		}
		allowed := true
		status := "未確認"
		if policyChecked {
			allowed = allowedIDs[item.ID]
			if allowed {
				status = "利用可"
			} else {
				status = "Privacy/Guardrail制限"
			}
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		out = append(out, openRouterPolicyModelSummary{
			ID: item.ID, Name: name, ContextLength: item.ContextLength,
			Pricing: pricing, PricingTiers: tiers,
			SupportedParameters: item.SupportedParameters, Free: free,
			PolicyChecked: policyChecked, PolicyAllowed: allowed,
			PolicyStatus: status, Moderated: item.TopProvider.IsModerated,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PolicyAllowed != out[j].PolicyAllowed {
			return out[i].PolicyAllowed
		}
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, map[string]any{
		"models": out, "count": len(out), "free_only": freeOnly,
		"policy_checked": policyChecked, "policy_error": policyError,
		"policy_source": "OpenRouter /models/user",
		"policy_scope": "provider preferences, privacy settings, model/provider guardrails; prompt-content filters and transient rate limits are not predictable from the catalog",
	})
}
