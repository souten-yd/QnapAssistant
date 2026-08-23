package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

func classifyOpenRouterVoiceError(err error) openRouterVoiceFailureClass {
	if err == nil {
		return openRouterVoiceFailureClass{}
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "http 429"):
		return classifyOpenRouterVoiceFailure(http.StatusTooManyRequests, message)
	case strings.Contains(lower, "http 404"):
		return classifyOpenRouterVoiceFailure(http.StatusNotFound, message)
	default:
		return openRouterVoiceFailureClass{Reason: message}
	}
}

func openRouterVoiceHealthScore(h openRouterVoiceModelHealth) float64 {
	// Beta(1,1) smoothing avoids over-promoting a model after one lucky request
	// while still preferring models with a demonstrated success history.
	return float64(h.SuccessCount+1) / float64(h.SuccessCount+h.FailureCount+2)
}

func (m *manager) openRouterVoiceRetryCandidates(ctx context.Context, c config, payload map[string]any, attempted map[string]bool) ([]openRouterFreeFallbackCandidate, error) {
	candidates, err := m.fetchOpenRouterFreeFallbackCandidates(ctx, c)
	if err != nil {
		return nil, err
	}
	health := m.openRouterVoiceHealthSnapshot()
	now := time.Now()
	out := make([]openRouterFreeFallbackCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if attempted[candidate.ID] || !fallbackCandidateSupportsPayload(candidate, payload) {
			continue
		}
		h := health[candidate.ID]
		if h.Blacklisted || openRouterVoiceCooldownActive(h, now) {
			continue
		}
		out = append(out, candidate)
	}
	// Randomize equal-quality candidates to avoid concentrating all requests on
	// one shared free-model pool, then stable-sort by observed reliability.
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	sort.SliceStable(out, func(i, j int) bool {
		hi, hj := health[out[i].ID], health[out[j].ID]
		si, sj := openRouterVoiceHealthScore(hi), openRouterVoiceHealthScore(hj)
		if si != sj {
			return si > sj
		}
		if hi.FailureCount != hj.FailureCount {
			return hi.FailureCount < hj.FailureCount
		}
		return out[i].ContextLength > out[j].ContextLength
	})
	return out, nil
}

func cloneOpenRouterVoiceConfig(c config) config {
	out := make(config, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

// streamVoiceLLMStandard is the session stream entry point used by
// /v1/voice/chat/stream. Generic OpenRouter requests keep the existing
// server-side `models` fallback chain, while this path uses explicit retries so
// each attempted model can be measured and blacklisted independently.
func (m *manager) streamVoiceLLMStandard(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, transcript string, controls voiceChatControls, llmStart time.Time) (<-chan string, <-chan voiceLLMContextStreamResult) {
	chunks := make(chan string, 8)
	result := make(chan voiceLLMContextStreamResult, 1)
	go func() {
		defer close(chunks)
		if normalizedLLMProvider(cfg) != "openrouter" || !openRouterAutoFreeFallbackEnabled(cfg) {
			innerChunks, innerResult := m.streamVoiceLLMStandardRaw(ctx, client, cfg, profile, transcript, controls, llmStart)
			for text := range innerChunks {
				select {
				case chunks <- text:
				case <-ctx.Done():
					result <- voiceLLMContextStreamResult{Err: ctx.Err()}
					return
				}
			}
			result <- <-innerResult
			return
		}

		maxAttempts := openRouterAutoFreeFallbackAttempts(cfg)
		primary := strings.TrimSpace(get(cfg, "OPENROUTER_MODEL", "openrouter/free"))
		if primary == "" {
			primary = "openrouter/free"
		}
		attempted := map[string]bool{}
		queue := []string{}
		health := m.openRouterVoiceHealthSnapshot()
		primaryHealth := health[primary]
		if !primaryHealth.Blacklisted && !openRouterVoiceCooldownActive(primaryHealth, time.Now()) {
			queue = append(queue, primary)
		} else {
			attempted[primary] = true
		}

		basePayload := voiceLLMPayloadStandard(cfg, transcript, true, controls)
		fallbackLoaded := false
		actualAttempts := 0
		var last voiceLLMContextStreamResult

		loadFallbacks := func() error {
			if fallbackLoaded {
				return nil
			}
			fallbackLoaded = true
			candidates, err := m.openRouterVoiceRetryCandidates(ctx, cfg, basePayload, attempted)
			if err != nil {
				return err
			}
			for _, candidate := range candidates {
				queue = append(queue, candidate.ID)
			}
			return nil
		}

		for actualAttempts < maxAttempts {
			if len(queue) == 0 {
				if err := loadFallbacks(); err != nil {
					if last.Err == nil {
						last.Err = fmt.Errorf("OpenRouter voice fallback candidate lookup failed: %w", err)
					}
					break
				}
			}
			if len(queue) == 0 {
				break
			}
			model := queue[0]
			queue = queue[1:]
			if attempted[model] {
				continue
			}
			attempted[model] = true
			actualAttempts++

			attemptCfg := cloneOpenRouterVoiceConfig(cfg)
			attemptCfg["OPENROUTER_MODEL"] = model
			attemptCfg["OPENROUTER_FALLBACK_MODELS"] = ""
			// Disable the generic server-side cross-model chain for this one
			// attempt. Provider failover within the same model remains unchanged.
			attemptCfg["OPENROUTER_AUTO_FREE_FALLBACK"] = "0"

			innerChunks, innerResult := m.streamVoiceLLMStandardRaw(ctx, client, attemptCfg, profile, transcript, controls, llmStart)
			emitted := false
			for text := range innerChunks {
				emitted = true
				select {
				case chunks <- text:
				case <-ctx.Done():
					result <- voiceLLMContextStreamResult{Err: ctx.Err()}
					return
				}
			}
			last = <-innerResult
			if last.Err == nil {
				m.recordOpenRouterVoiceSuccess(model)
				result <- last
				return
			}

			class := classifyOpenRouterVoiceError(last.Err)
			m.recordOpenRouterVoiceFailure(model, class)
			if emitted || !class.Retry {
				result <- last
				return
			}
			if err := loadFallbacks(); err != nil && len(queue) == 0 {
				last.Err = fmt.Errorf("%v; fallback lookup failed: %w", last.Err, err)
				break
			}
		}
		if last.Err == nil {
			last.Err = fmt.Errorf("OpenRouter voice fallback exhausted: no eligible free model")
		}
		result <- last
	}()
	return chunks, result
}
