package main

import (
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
	ID                  string                     `json:"id"`
	Name                string                     `json:"name"`
	ContextLength       int                        `json:"context_length"`
	Pricing             map[string]json.RawMessage `json:"pricing"`
	SupportedParameters []string                   `json:"supported_parameters"`
}

// scalarOpenRouterPricing keeps only scalar pricing values that the existing UI
// can display. OpenRouter may add structured pricing entries (arrays/objects)
// for modalities or tools; those must not make the whole model catalog fail.
func scalarOpenRouterPricing(raw map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for key, value := range raw {
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			out[key] = s
			continue
		}
		var n json.Number
		if err := json.Unmarshal(value, &n); err == nil && n.String() != "" {
			out[key] = n.String()
		}
	}
	return out
}

func scalarPriceZero(pricing map[string]string, key string) bool {
	v := strings.TrimSpace(pricing[key])
	if v == "" {
		return false
	}
	n, err := strconv.ParseFloat(v, 64)
	return err == nil && n == 0
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
	out := make([]openRouterModelSummary, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		pricing := scalarOpenRouterPricing(item.Pricing)
		free := strings.Contains(item.ID, ":free") || (scalarPriceZero(pricing, "prompt") && scalarPriceZero(pricing, "completion"))
		if freeOnly && !free {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		out = append(out, openRouterModelSummary{
			ID: item.ID, Name: name, ContextLength: item.ContextLength,
			Pricing: pricing, SupportedParameters: item.SupportedParameters, Free: free,
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
