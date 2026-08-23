package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// applyOpenRouterRequestConfig makes the server-side provider selection the
// source of truth for generic /v1/chat/completions callers as well as voice.
// Non-chat OpenAI-compatible endpoints are proxied unchanged.
func applyOpenRouterRequestConfig(r *http.Request, cfg config) error {
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
	applyOpenRouterPayload(cfg, payload)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	r.Header.Del("Content-Length")
	return nil
}
