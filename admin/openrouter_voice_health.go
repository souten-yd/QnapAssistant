package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type openRouterVoiceModelHealth struct {
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

type openRouterVoiceHealthStore struct {
	Version int                                    `json:"version"`
	Models  map[string]*openRouterVoiceModelHealth `json:"models"`
}

type openRouterVoiceFailureClass struct {
	Status    int
	Retry     bool
	Blacklist bool
	Reason    string
	Cooldown time.Duration
}

var openRouterVoiceHealthMu sync.Mutex

func (m *manager) openRouterVoiceHealthPath() string {
	return filepath.Join(filepath.Dir(m.configPath), "openrouter-voice-health.json")
}

func emptyOpenRouterVoiceHealthStore() openRouterVoiceHealthStore {
	return openRouterVoiceHealthStore{Version: 1, Models: map[string]*openRouterVoiceModelHealth{}}
}

func (m *manager) loadOpenRouterVoiceHealthLocked() openRouterVoiceHealthStore {
	store := emptyOpenRouterVoiceHealthStore()
	b, err := os.ReadFile(m.openRouterVoiceHealthPath())
	if err != nil {
		return store
	}
	if json.Unmarshal(b, &store) != nil || store.Models == nil {
		return emptyOpenRouterVoiceHealthStore()
	}
	return store
}

func (m *manager) saveOpenRouterVoiceHealthLocked(store openRouterVoiceHealthStore) error {
	if store.Models == nil {
		store.Models = map[string]*openRouterVoiceModelHealth{}
	}
	store.Version = 1
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := m.openRouterVoiceHealthPath()
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

func copyOpenRouterVoiceHealth(h *openRouterVoiceModelHealth) openRouterVoiceModelHealth {
	if h == nil {
		return openRouterVoiceModelHealth{}
	}
	return *h
}

func (m *manager) openRouterVoiceHealthSnapshot() map[string]openRouterVoiceModelHealth {
	openRouterVoiceHealthMu.Lock()
	defer openRouterVoiceHealthMu.Unlock()
	store := m.loadOpenRouterVoiceHealthLocked()
	out := make(map[string]openRouterVoiceModelHealth, len(store.Models))
	for id, h := range store.Models {
		out[id] = copyOpenRouterVoiceHealth(h)
	}
	return out
}

func openRouterVoiceCooldownActive(h openRouterVoiceModelHealth, now time.Time) bool {
	if h.CooldownUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, h.CooldownUntil)
	return err == nil && now.Before(until)
}

func (m *manager) recordOpenRouterVoiceSuccess(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	openRouterVoiceHealthMu.Lock()
	defer openRouterVoiceHealthMu.Unlock()
	store := m.loadOpenRouterVoiceHealthLocked()
	h := store.Models[model]
	if h == nil {
		h = &openRouterVoiceModelHealth{}
		store.Models[model] = h
	}
	h.SuccessCount++
	h.LastSuccessAt = time.Now().UTC().Format(time.RFC3339)
	h.CooldownUntil = ""
	_ = m.saveOpenRouterVoiceHealthLocked(store)
}

func (m *manager) recordOpenRouterVoiceFailure(model string, class openRouterVoiceFailureClass) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	openRouterVoiceHealthMu.Lock()
	defer openRouterVoiceHealthMu.Unlock()
	store := m.loadOpenRouterVoiceHealthLocked()
	h := store.Models[model]
	if h == nil {
		h = &openRouterVoiceModelHealth{}
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
	_ = m.saveOpenRouterVoiceHealthLocked(store)
}

func openRouterVoicePolicyFailureReason(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	policySignals := []string{
		"guardrail restrictions", "data policy", "privacy", "policy restriction",
		"zero data retention", "zdr",
	}
	for _, signal := range policySignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func classifyOpenRouterVoiceFailure(status int, message string) openRouterVoiceFailureClass {
	class := openRouterVoiceFailureClass{Status: status, Reason: strings.TrimSpace(message)}
	switch status {
	case http.StatusTooManyRequests:
		class.Retry = true
		class.Cooldown = 2 * time.Minute
		return class
	case http.StatusNotFound:
		class.Retry = true
		// Only explicit endpoint-policy signals deserve a persistent blacklist.
		// A plain "no endpoints" response can be transient availability.
		if openRouterVoicePolicyFailureReason(message) {
			class.Blacklist = true
			return class
		}
		class.Cooldown = 10 * time.Minute
		return class
	default:
		return class
	}
}

func (m *manager) clearOpenRouterVoiceBlacklist(model string) error {
	openRouterVoiceHealthMu.Lock()
	defer openRouterVoiceHealthMu.Unlock()
	store := m.loadOpenRouterVoiceHealthLocked()
	model = strings.TrimSpace(model)
	if model == "" {
		for _, h := range store.Models {
			if h == nil {
				continue
			}
			h.Blacklisted = false
			h.BlacklistReason = ""
			h.CooldownUntil = ""
		}
	} else if h := store.Models[model]; h != nil {
		h.Blacklisted = false
		h.BlacklistReason = ""
		h.CooldownUntil = ""
	}
	return m.saveOpenRouterVoiceHealthLocked(store)
}

// resetOpenRouterVoicePolicyState is intentionally narrower than a full history
// reset. OpenRouter does not expose a policy revision id, and /models/user can
// also change when models are added or removed. Therefore policy changes are
// re-evaluated explicitly from the UI: policy-derived blacklists are cleared,
// while successful/failed usage counts remain useful for ranking.
func (m *manager) resetOpenRouterVoicePolicyState() error {
	openRouterVoiceHealthMu.Lock()
	store := m.loadOpenRouterVoiceHealthLocked()
	for _, h := range store.Models {
		if h == nil || !h.Blacklisted || !openRouterVoicePolicyFailureReason(h.BlacklistReason) {
			continue
		}
		h.Blacklisted = false
		h.BlacklistReason = ""
		h.CooldownUntil = ""
	}
	err := m.saveOpenRouterVoiceHealthLocked(store)
	openRouterVoiceHealthMu.Unlock()

	// Force the next fallback/policy request to re-read /models/user rather than
	// reusing the five-minute candidate cache.
	openRouterFreeFallbackCache.Lock()
	openRouterFreeFallbackCache.cacheKey = ""
	openRouterFreeFallbackCache.at = time.Time{}
	openRouterFreeFallbackCache.models = nil
	openRouterFreeFallbackCache.Unlock()
	return err
}

func (m *manager) resetOpenRouterVoiceHistory() error {
	openRouterVoiceHealthMu.Lock()
	defer openRouterVoiceHealthMu.Unlock()
	return m.saveOpenRouterVoiceHealthLocked(emptyOpenRouterVoiceHealthStore())
}

func (m *manager) handleOpenRouterVoiceHealth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snapshot := m.openRouterVoiceHealthSnapshot()
		ids := make([]string, 0, len(snapshot))
		for id := range snapshot {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]any{"model": id, "health": snapshot[id]})
		}
		writeJSON(w, map[string]any{"models": rows})
	case http.MethodPost:
		if !sameOriginUpdateRequest(r) {
			http.Error(w, "cross-origin voice-health mutation refused", http.StatusForbidden)
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
		var err error
		switch req.Action {
		case "clear_blacklist":
			err = m.clearOpenRouterVoiceBlacklist(req.Model)
		case "reset_policy_state":
			err = m.resetOpenRouterVoicePolicyState()
		case "reset_history":
			err = m.resetOpenRouterVoiceHistory()
		default:
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "action": req.Action, "model": strings.TrimSpace(req.Model)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
