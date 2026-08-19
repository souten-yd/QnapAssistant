package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func normalizeThinkingMode(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "on", "passthrough":
		return v, true
	default:
		return "", false
	}
}

func (m *manager) handleThinking(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		mode, ok := normalizeThinkingMode(cfg["THINKING_MODE"])
		if !ok {
			mode = "off"
		}
		writeJSON(w, map[string]any{"mode": mode})
	case http.MethodPut:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		mode, ok := normalizeThinkingMode(req.Mode)
		if !ok {
			http.Error(w, "mode must be off, on, or passthrough", http.StatusBadRequest)
			return
		}
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		cfg["THINKING_MODE"] = mode
		if err := saveConfig(m.configPath, cfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "mode": mode})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *manager) handleProxyWithThinking(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	if err := applyThinkingMode(r, cfg); err != nil {
		http.Error(w, "thinking mode request rewrite failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	m.handleProxy(w, r)
}

func applyThinkingMode(r *http.Request, cfg config) error {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Body == nil {
		return nil
	}
	mode, ok := normalizeThinkingMode(cfg["THINKING_MODE"])
	if !ok || mode == "passthrough" {
		return nil
	}
	if !strings.Contains(strings.ToLower(filepath.Base(cfg["MODEL_PATH"])), "qwen3") {
		return nil
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	restore := func() {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		restore()
		return nil
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 || messagesHaveThinkingDirective(messages) {
		restore()
		return nil
	}

	directive := "/no_think"
	if mode == "on" {
		directive = "/think"
	}
	if !appendThinkingDirective(messages, directive) {
		restore()
		return nil
	}

	rewritten, err := json.Marshal(payload)
	if err != nil {
		restore()
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	r.Header.Del("Content-Length")
	return nil
}

func messagesHaveThinkingDirective(messages []any) bool {
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if contentHasThinkingDirective(msg["content"]) {
			return true
		}
	}
	return false
}

func contentHasThinkingDirective(content any) bool {
	switch v := content.(type) {
	case string:
		s := strings.ToLower(v)
		return strings.Contains(s, "/no_think") || strings.Contains(s, "/think")
	case []any:
		for _, part := range v {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := p["text"].(string); ok && contentHasThinkingDirective(text) {
				return true
			}
		}
	}
	return false
}

func appendThinkingDirective(messages []any, directive string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			msg["content"] = strings.TrimRight(content, " \t\r\n") + " " + directive
			return true
		case []any:
			msg["content"] = append(content, map[string]any{"type": "text", "text": directive})
			return true
		}
	}
	return false
}
