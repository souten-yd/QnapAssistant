package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	openRouterAutoFallbackDefaultAttempts = 3
	openRouterAutoFallbackMaxAttempts     = 5
)

type openRouterFreeFallbackCandidate struct {
	ID                  string
	Name                string
	ContextLength       int
	SupportedParameters []string
}

var openRouterFreeFallbackCache struct {
	sync.Mutex
	baseURL string
	at      time.Time
	models  []openRouterFreeFallbackCandidate
}

func openRouterAutoFreeFallbackEnabled(c config) bool {
	return boolConfig(c, "OPENROUTER_AUTO_FREE_FALLBACK", false)
}

// The attempt count includes the configured primary model. Keeping the hard
// ceiling at five limits latency and protects OpenRouter's free-request quota,
// where failed attempts can still count against request limits.
func openRouterAutoFreeFallbackAttempts(c config) int {
	n := intVal(c, "OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS", openRouterAutoFallbackDefaultAttempts)
	if n < 1 {
		return 1
	}
	if n > openRouterAutoFallbackMaxAttempts {
		return openRouterAutoFallbackMaxAttempts
	}
	return n
}

func copyFreeFallbackCandidates(in []openRouterFreeFallbackCandidate) []openRouterFreeFallbackCandidate {
	return append([]openRouterFreeFallbackCandidate(nil), in...)
}

func (m *manager) fetchOpenRouterFreeFallbackCandidates(ctx context.Context, c config) ([]openRouterFreeFallbackCandidate, error) {
	base := openRouterBaseURL(c)
	openRouterFreeFallbackCache.Lock()
	if openRouterFreeFallbackCache.baseURL == base && !openRouterFreeFallbackCache.at.IsZero() && time.Since(openRouterFreeFallbackCache.at) < 5*time.Minute {
		out := copyFreeFallbackCandidates(openRouterFreeFallbackCache.models)
		openRouterFreeFallbackCache.Unlock()
		return out, nil
	}
	openRouterFreeFallbackCache.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, base+"/models?output_modalities=text", nil)
	if err != nil {
		return nil, err
	}
	if key, _ := m.readOpenRouterKey(); key != "" {
		m.applyOpenRouterHeaders(req, key, c)
	}
	resp, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("OpenRouter models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []openRouterCatalogModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&decoded); err != nil {
		return nil, err
	}
	out := make([]openRouterFreeFallbackCandidate, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		pricing, tiers, err := parseOpenRouterPricing(item.Pricing)
		if err != nil {
			continue
		}
		if !strings.Contains(item.ID, ":free") && !pricingIsFree(pricing, tiers) {
			continue
		}
		id := strings.TrimSpace(item.ID)
		if id == "" || id == "openrouter/free" {
			continue
		}
		out = append(out, openRouterFreeFallbackCandidate{
			ID: id, Name: strings.TrimSpace(item.Name), ContextLength: item.ContextLength,
			SupportedParameters: append([]string(nil), item.SupportedParameters...),
		})
	}
	// Prefer larger-context fallbacks first. There is no reliable quality score
	// in /models, and context headroom avoids turning one availability failure
	// into a context-length failure on the next model.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ContextLength != out[j].ContextLength {
			return out[i].ContextLength > out[j].ContextLength
		}
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	openRouterFreeFallbackCache.Lock()
	openRouterFreeFallbackCache.baseURL = base
	openRouterFreeFallbackCache.at = time.Now()
	openRouterFreeFallbackCache.models = copyFreeFallbackCandidates(out)
	openRouterFreeFallbackCache.Unlock()
	return out, nil
}

func payloadNeedsSupportedParameter(payload map[string]any, key string) bool {
	v, ok := payload[key]
	if !ok || v == nil {
		return false
	}
	if a, ok := v.([]any); ok {
		return len(a) > 0
	}
	return true
}

func fallbackCandidateSupportsPayload(candidate openRouterFreeFallbackCandidate, payload map[string]any) bool {
	if len(candidate.SupportedParameters) == 0 {
		return true
	}
	supported := map[string]bool{}
	for _, p := range candidate.SupportedParameters {
		supported[p] = true
	}
	for _, required := range []string{"tools", "tool_choice", "response_format", "reasoning", "reasoning_effort"} {
		if payloadNeedsSupportedParameter(payload, required) && !supported[required] {
			// Some models advertise structured_outputs instead of response_format.
			if required == "response_format" && supported["structured_outputs"] {
				continue
			}
			return false
		}
	}
	return true
}

func setOpenRouterModelChain(payload map[string]any, models []string) {
	if len(models) <= 1 {
		delete(payload, "models")
		if len(models) == 1 {
			payload["model"] = models[0]
		}
		return
	}
	delete(payload, "model")
	payload["models"] = models
}

// applyOpenRouterAutoFreeFallback uses OpenRouter's model-level `models` array
// rather than issuing duplicate application-level HTTP retries. When enabled it
// deliberately replaces the manual model fallback chain for this request:
// the configured primary is tried first, followed only by distinct free models.
// Turning the feature off restores the existing manual OPENROUTER_FALLBACK_MODELS behavior.
func (m *manager) applyOpenRouterAutoFreeFallback(ctx context.Context, c config, payload map[string]any) {
	if !openRouterAutoFreeFallbackEnabled(c) {
		return
	}
	maxAttempts := openRouterAutoFreeFallbackAttempts(c)
	primary := strings.TrimSpace(get(c, "OPENROUTER_MODEL", "openrouter/free"))
	if primary == "" {
		primary = "openrouter/free"
	}
	chain := []string{primary}
	if maxAttempts <= 1 {
		setOpenRouterModelChain(payload, chain)
		return
	}
	seen := map[string]bool{primary: true}
	candidates, err := m.fetchOpenRouterFreeFallbackCandidates(ctx, c)
	if err == nil {
		for _, candidate := range candidates {
			if len(chain) >= maxAttempts {
				break
			}
			if seen[candidate.ID] || !fallbackCandidateSupportsPayload(candidate, payload) {
				continue
			}
			seen[candidate.ID] = true
			chain = append(chain, candidate.ID)
		}
	}
	// Dynamic free router is a useful floor when the catalog is temporarily
	// unavailable or there are fewer compatible explicit free variants.
	if len(chain) < maxAttempts && !seen["openrouter/free"] {
		chain = append(chain, "openrouter/free")
	}
	setOpenRouterModelChain(payload, chain)
}
