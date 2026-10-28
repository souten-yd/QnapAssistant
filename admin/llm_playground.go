package main

import (
    "bytes"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net"
    "net/http"
    "strconv"
    "strings"
    "time"
)

// This endpoint never forwards to an external provider. It is intentionally
// separate from the provider-aware /v1 proxy so pasted health data stays local.
type localTestInput struct {
    SystemPrompt string `json:"system_prompt"`
    Prompt       string `json:"prompt"`
    MaxTokens    int    `json:"max_tokens"`
}

func validateLocalTestInput(in localTestInput) error {
    if strings.TrimSpace(in.Prompt) == "" {
        return errors.New("入力テキストを指定してください")
    }
    if len(in.Prompt) > 192000 || len(in.SystemPrompt) > 12000 {
        return errors.New("入力が大きすぎます。1週間分を集約して再試行してください")
    }
    if in.MaxTokens < 64 || in.MaxTokens > 2048 {
        return errors.New("max_tokens must be between 64 and 2048")
    }
    return nil
}

func (m *manager) handleLocalLLMTest(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }
    cfg, err := loadConfig(m.configPath)
    if err != nil {
        http.Error(w, "local configuration unavailable", http.StatusInternalServerError)
        return
    }
    cfg = defaults(cfg)
    if normalizedLLMProvider(cfg) != "local" {
        http.Error(w, "ローカルモデルを選択してからテストしてください。OpenRouterには送信しません", http.StatusConflict)
        return
    }

    var in localTestInput
    dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
    dec.DisallowUnknownFields()
    if err := dec.Decode(&in); err != nil {
        http.Error(w, "invalid test input: "+err.Error(), http.StatusBadRequest)
        return
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        http.Error(w, "test input must contain exactly one JSON object", http.StatusBadRequest)
        return
    }
    if err := validateLocalTestInput(in); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    messages := make([]map[string]string, 0, 2)
    if strings.TrimSpace(in.SystemPrompt) != "" {
        messages = append(messages, map[string]string{"role": "system", "content": in.SystemPrompt})
    }
    messages = append(messages, map[string]string{"role": "user", "content": in.Prompt})
    payload, err := json.Marshal(map[string]any{
        "messages": messages,
        "max_tokens": in.MaxTokens,
        "temperature": 0.2,
        "stream": false,
    })
    if err != nil {
        http.Error(w, "could not prepare test request", http.StatusInternalServerError)
        return
    }

    // Do not call the provider-aware proxy. This request targets loopback only.
    target := "http://" + net.JoinHostPort("127.0.0.1", cfg["BACKEND_PORT"]) + "/v1/chat/completions"
    upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(payload))
    if err != nil {
        http.Error(w, "invalid local backend settings", http.StatusInternalServerError)
        return
    }
    upstream.Header.Set("Content-Type", "application/json")
    if err := applyThinkingMode(upstream, cfg); err != nil {
        http.Error(w, "thinking mode configuration failed", http.StatusBadRequest)
        return
    }

    if err := m.ensureReady(r.Context()); err != nil {
        http.Error(w, "local LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
        return
    }
    // If configuration changed while warming up, fail closed rather than
    // allowing a provider-aware request to send data off NAS.
    latest, err := loadConfig(m.configPath)
    if err != nil || normalizedLLMProvider(defaults(latest)) != "local" {
        http.Error(w, "local provider changed during model preparation", http.StatusConflict)
        return
    }
    release := m.beginLLMUse()
    defer release()

    client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
        Proxy: nil,
        DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
    }}
    defer client.CloseIdleConnections()
    response, err := client.Do(upstream)
    if err != nil {
        if r.Context().Err() != nil {
            return
        }
        http.Error(w, "local inference failed: "+err.Error(), http.StatusBadGateway)
        return
    }
    defer response.Body.Close()

    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.Header().Set("Cache-Control", "no-store")
    w.Header().Set("X-Content-Type-Options", "nosniff")
    w.Header().Set("X-Local-Model-Test", "1")
    w.WriteHeader(response.StatusCode)
    // Do not log or store prompts/responses; never proxy raw model bytes to
    // arbitrary URLs and bound the response read.
    _, _ = io.Copy(w, io.LimitReader(response.Body, 1<<20))
}

func localTestDescription(c config) string {
    return fmt.Sprintf("%s / ctx %s", c["MODEL_PATH"], strconv.Itoa(intVal(c, "CONTEXT", 4096)))
}
