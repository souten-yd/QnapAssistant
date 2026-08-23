package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type openRouterModelHealth struct {
	SuccessCount      int    `json:"success_count"`
	FailureCount      int    `json:"failure_count"`
	LastSuccessAt     string `json:"last_success_at,omitempty"`
	LastFailureAt     string `json:"last_failure_at,omitempty"`
	LastFailureReason string `json:"last_failure_reason,omitempty"`
	LastHTTPStatus    int    `json:"last_http_status,omitempty"`
	Blacklisted       bool   `json:"blacklisted"`
	BlacklistReason   string `json:"blacklist_reason,omitempty"`
	CooldownUntil     string `json:"cooldown_until,omitempty"`
}

type openRouterFallbackStore struct {
	Version int                               `json:"version"`
	Models  map[string]*openRouterModelHealth `json:"models"`
}

type openRouterRetryClass struct {
	Status    int
	Retry     bool
	Blacklist bool
	Reason    string
	Cooldown time.Duration
}

var openRouterFallbackMu sync.Mutex

func (m *manager) openRouterFallbackPath() string {
	return filepath.Join(filepath.Dir(m.configPath), "openrouter-fallback.json")
}

func emptyOpenRouterFallbackStore() openRouterFallbackStore {
	return openRouterFallbackStore{Version: 1, Models: map[string]*openRouterModelHealth{}}
}

func (m *manager) loadOpenRouterFallbackStoreLocked() openRouterFallbackStore {
	store := emptyOpenRouterFallbackStore()
	b, err := os.ReadFile(m.openRouterFallbackPath())
	if err != nil {
		return store
	}
	if json.Unmarshal(b, &store) != nil || store.Models == nil {
		return emptyOpenRouterFallbackStore()
	}
	return store
}

func (m *manager) saveOpenRouterFallbackStoreLocked(store openRouterFallbackStore) error {
	if store.Models == nil {
		store.Models = map[string]*openRouterModelHealth{}
	}
	store.Version = 1
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := m.openRouterFallbackPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
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

func cloneOpenRouterHealth(in *openRouterModelHealth) openRouterModelHealth {
	if in == nil {
		return openRouterModelHealth{}
	}
	return *in
}

func (m *manager) openRouterFallbackSnapshot() map[string]openRouterModelHealth {
	openRouterFallbackMu.Lock()
	defer openRouterFallbackMu.Unlock()
	store := m.loadOpenRouterFallbackStoreLocked()
	out := make(map[string]openRouterModelHealth, len(store.Models))
	for id, health := range store.Models {
		out[id] = cloneOpenRouterHealth(health)
	}
	return out
}

func healthCooldownActive(h openRouterModelHealth, now time.Time) bool {
	if h.CooldownUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, h.CooldownUntil)
	return err == nil && now.Before(until)
}

func (m *manager) recordOpenRouterModelSuccess(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	openRouterFallbackMu.Lock()
	defer openRouterFallbackMu.Unlock()
	store := m.loadOpenRouterFallbackStoreLocked()
	h := store.Models[model]
	if h == nil {
		h = &openRouterModelHealth{}
		store.Models[model] = h
	}
	h.SuccessCount++
	h.LastSuccessAt = time.Now().UTC().Format(time.RFC3339)
	h.CooldownUntil = ""
	_ = m.saveOpenRouterFallbackStoreLocked(store)
}

func (m *manager) recordOpenRouterModelFailure(model string, class openRouterRetryClass) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	openRouterFallbackMu.Lock()
	defer openRouterFallbackMu.Unlock()
	store := m.loadOpenRouterFallbackStoreLocked()
	h := store.Models[model]
	if h == nil {
		h = &openRouterModelHealth{}
		store.Models[model] = h
	}
	h.FailureCount++
	h.LastFailureAt = time.Now().UTC().Format(time.RFC3339)
	h.LastFailureReason = class.Reason
	h.LastHTTPStatus = class.Status
	if class.Cooldown > 0 {
		h.CooldownUntil = time.Now().UTC().Add(class.Cooldown).Format(time.RFC3339)
	}
	if class.Blacklist {
		h.Blacklisted = true
		h.BlacklistReason = class.Reason
		h.CooldownUntil = ""
	}
	_ = m.saveOpenRouterFallbackStoreLocked(store)
}

func (m *manager) markOpenRouterPolicyFiltered(model, reason string) {
	class := openRouterRetryClass{Status: http.StatusNotFound, Retry: true, Blacklist: true, Reason: reason}
	m.recordOpenRouterModelFailure(model, class)
}

func classifyOpenRouterRetryError(err error) openRouterRetryClass {
	if err == nil {
		return openRouterRetryClass{}
	}
	s := strings.ToLower(err.Error())
	class := openRouterRetryClass{Reason: strings.TrimSpace(err.Error())}
	if strings.Contains(s, "http 429") {
		class.Status = http.StatusTooManyRequests
		class.Retry = true
		class.Cooldown = 2 * time.Minute
		return class
	}
	if !strings.Contains(s, "http 404") {
		return class
	}
	class.Status = http.StatusNotFound
	class.Retry = true
	policyWords := []string{
		"guardrail", "data policy", "privacy", "policy restriction", "policy restrictions",
		"no endpoints available matching", "zero data retention", "zdr",
	}
	for _, word := range policyWords {
		if strings.Contains(s, word) {
			class.Blacklist = true
			return class
		}
	}
	// A generic 404 can be transient endpoint availability or a recently removed
	// model. Do not permanently blacklist it without a policy signal.
	class.Cooldown = 10 * time.Minute
	return class
}

func openRouterVoiceFallbackEnabled(c config) bool {
	return boolConfig(c, "OPENROUTER_VOICE_AUTO_FALLBACK", true)
}

func openRouterVoiceFallbackMaxAttempts(c config) int {
	n := intVal(c, "OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS", 3)
	if n < 1 {
		return 1
	}
	if n > 5 {
		return 5
	}
	return n
}

func cloneConfig(c config) config {
	out := make(config, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}

func (m *manager) healthyOpenRouterFallbackCandidates(ctx context.Context, c config, exclude map[string]bool) ([]string, error) {
	models, err := m.fetchOpenRouterUserFreeModelIDs(ctx, c)
	if err != nil {
		return nil, err
	}
	health := m.openRouterFallbackSnapshot()
	now := time.Now()
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model == "" || exclude[model] {
			continue
		}
		h := health[model]
		if h.Blacklisted || healthCooldownActive(h, now) {
			continue
		}
		out = append(out, model)
	}
	// Randomize equivalent healthy free models so a shared upstream pool is not
	// hammered in a fixed order. Historical health is still persisted/displayed.
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out, nil
}

// streamVoiceLLMStandardWithFallback wraps the existing stream implementation.
// It retries only before any text chunk has been emitted; switching models after
// speech has started would splice two answers together and is intentionally not
// attempted.
func (m *manager) streamVoiceLLMStandardWithFallback(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, transcript string, controls voiceChatControls, llmStart time.Time) (<-chan string, <-chan voiceLLMContextStreamResult) {
	chunks := make(chan string, 8)
	result := make(chan voiceLLMContextStreamResult, 1)
	go func() {
		defer close(chunks)
		primary := strings.TrimSpace(get(cfg, "OPENROUTER_MODEL", "openrouter/free"))
		if normalizedLLMProvider(cfg) != "openrouter" || !openRouterVoiceFallbackEnabled(cfg) {
			innerChunks, innerResult := m.streamVoiceLLMStandard(ctx, client, cfg, profile, transcript, controls, llmStart)
			for text := range innerChunks {
				chunks <- text
			}
			result <- <-innerResult
			return
		}

		maxAttempts := openRouterVoiceFallbackMaxAttempts(cfg)
		attempted := map[string]bool{}
		candidates := []string{primary}
		fallbacks, _ := m.healthyOpenRouterFallbackCandidates(ctx, cfg, attempted)
		for _, model := range fallbacks {
			if model != primary {
				candidates = append(candidates, model)
			}
		}
		if len(candidates) > maxAttempts {
			candidates = candidates[:maxAttempts]
		}

		var last voiceLLMContextStreamResult
		for _, model := range candidates {
			if attempted[model] {
				continue
			}
			attempted[model] = true
			attemptCfg := cloneConfig(cfg)
			attemptCfg["OPENROUTER_MODEL"] = model
			// Keep provider failover within the selected model, but make model
			// fallback explicit here so health/blacklist accounting stays accurate.
			attemptCfg["OPENROUTER_FALLBACK_MODELS"] = ""
			innerChunks, innerResult := m.streamVoiceLLMStandard(ctx, client, attemptCfg, profile, transcript, controls, llmStart)
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
				m.recordOpenRouterModelSuccess(model)
				result <- last
				return
			}
			class := classifyOpenRouterRetryError(last.Err)
			m.recordOpenRouterModelFailure(model, class)
			if emitted || !class.Retry {
				result <- last
				return
			}
		}
		if last.Err == nil {
			last.Err = fmt.Errorf("OpenRouter voice fallback exhausted without a reply")
		}
		result <- last
	}()
	return chunks, result
}

func (m *manager) clearOpenRouterBlacklist(model string) error {
	openRouterFallbackMu.Lock()
	defer openRouterFallbackMu.Unlock()
	store := m.loadOpenRouterFallbackStoreLocked()
	if model != "" {
		if h := store.Models[model]; h != nil {
			h.Blacklisted = false
			h.BlacklistReason = ""
			h.CooldownUntil = ""
		}
	} else {
		for _, h := range store.Models {
			if h == nil {
				continue
			}
			h.Blacklisted = false
			h.BlacklistReason = ""
			h.CooldownUntil = ""
		}
	}
	return m.saveOpenRouterFallbackStoreLocked(store)
}

func (m *manager) handleOpenRouterFallback(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	switch r.Method {
	case http.MethodGet:
		snapshot := m.openRouterFallbackSnapshot()
		ids := make([]string, 0, len(snapshot))
		for id := range snapshot {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			h := snapshot[id]
			rows = append(rows, map[string]any{"model": id, "health": h})
		}
		writeJSON(w, map[string]any{
			"enabled": openRouterVoiceFallbackEnabled(cfg), "max_attempts": openRouterVoiceFallbackMaxAttempts(cfg),
			"models": rows,
		})
	case http.MethodPost:
		if !sameOriginUpdateRequest(r) {
			http.Error(w, "cross-origin fallback mutation refused", http.StatusForbidden)
			return
		}
		var req struct {
			Action string `json:"action"`
			Model  string `json:"model"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Action != "clear_blacklist" {
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		if err := m.clearOpenRouterBlacklist(strings.TrimSpace(req.Model)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "model": strings.TrimSpace(req.Model)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func parsePositiveInt(value string, d int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return d
	}
	return n
}
