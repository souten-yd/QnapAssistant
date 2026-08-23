package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// applyOpenRouterRequestConfig makes the server-side provider/model selection
// the source of truth for generic /v1/chat/completions callers. Optional
// generation controls remain transparent unless QnapAssistant explicitly
// configures an override.
func (m *manager) applyOpenRouterRequestConfig(r *http.Request, cfg config) error {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Preserve the original body so OpenRouter can return its own schema
		// error rather than turning a transparent proxy into a JSON parser gate.
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return nil
	}
	originalReasoningEffort, hadReasoningEffort := payload["reasoning_effort"]
	applyOpenRouterPayload(cfg, payload)
	m.applyOpenRouterAutoFreeFallback(r.Context(), cfg, payload)
	if strings.TrimSpace(cfg["OPENROUTER_REASONING_EFFORT"]) == "" && hadReasoningEffort {
		payload["reasoning_effort"] = originalReasoningEffort
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	r.Header.Del("Content-Length")
	return nil
}
