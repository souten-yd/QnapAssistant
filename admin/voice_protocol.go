package main

import "net/http"

func (m *manager) handleVoiceProtocol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	limit := any("backend_default")
	if n := intVal(cfg, "VOICE_REPLY_MAX_TOKENS", 0); n > 0 {
		limit = n
	}
	writeJSON(w, map[string]any{
		"version": "0.4.2",
		"endpoints": map[string]any{
			"chat":        "/v1/voice/chat",
			"chat_stream": "/v1/voice/chat/stream",
		},
		"utterance_boundary": "request_body_end",
		"request_controls": map[string]any{
			"header": "X-Qnap-Voice-Context",
			"header_encoding": "base64url(json), padding optional",
			"fields": []string{"system", "max_tokens", "history", "messages", "session_id", "reset_session"},
			"query_fields": []string{"system", "max_tokens", "history", "session_id", "reset_session", "profile"},
			"max_tokens": map[string]any{
				"default": limit,
				"meaning": "completion-wide limit; omitted/0 configured default means inherit backend standard",
				"explicit_range": "1..2048",
			},
		},
		"history": map[string]any{
			"inline_max_messages": voiceMaxHistoryItems,
			"session_recent_messages": voiceSessionRecentMessages,
			"session_storage": voiceSessionDir(cfg),
			"session_ttl_hours": int(voiceSessionTTL.Hours()),
		},
		"streaming": map[string]any{
			"llm": "SSE from llama.cpp",
			"tts": "each text chunk is synthesized independently while later LLM output continues",
			"m5go": map[string]any{
				"format": "multipart/mixed",
				"sample_rate": intVal(cfg, "VOICE_M5_SAMPLE_RATE", 16000),
				"chunk_min_chars": intVal(cfg, "VOICE_M5_CHUNK_MIN_CHARS", 8),
				"chunk_max_chars": intVal(cfg, "VOICE_M5_CHUNK_MAX_CHARS", 18),
				"note": "chunk character bounds are transport/TTS units, not an answer-length limit",
			},
		},
	})
}
