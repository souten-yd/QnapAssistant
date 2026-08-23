package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func configKeyAllowedV04(k string) bool {
	allowed := map[string]bool{
		"LLM_PROVIDER": true, "LLM_AUTO_UNLOAD": true, "LLM_IDLE_TIMEOUT_SECONDS": true,
		"MODEL_PATH": true, "MODEL_DIR": true, "MODEL_URL": true, "MODEL_SHA256": true, "MIN_MODEL_BYTES": true,
		"ADMIN_PORT": true, "BACKEND_PORT": true, "THREADS": true, "THREADS_BATCH": true, "CONTEXT": true, "BATCH": true, "UBATCH": true, "PARALLEL": true,
		"IDLE_TIMEOUT_SECONDS": true, "EXTRA_ARGS": true,
		"OPENROUTER_BASE_URL": true, "OPENROUTER_MODEL": true, "OPENROUTER_FALLBACK_MODELS": true, "OPENROUTER_PRESET": true,
		"OPENROUTER_TEMPERATURE": true, "OPENROUTER_TOP_P": true, "OPENROUTER_REASONING_EFFORT": true,
		"OPENROUTER_PROVIDER_SORT": true, "OPENROUTER_ALLOW_FALLBACKS": true, "OPENROUTER_REQUIRE_PARAMETERS": true, "OPENROUTER_DATA_COLLECTION": true, "OPENROUTER_ZDR": true,
		"OPENROUTER_PROVIDER_ONLY": true, "OPENROUTER_PROVIDER_IGNORE": true, "OPENROUTER_MAX_PRICE_PROMPT": true, "OPENROUTER_MAX_PRICE_COMPLETION": true,
		"OPENROUTER_HTTP_REFERER": true, "OPENROUTER_X_TITLE": true,
		"OPENROUTER_VOICE_AUTO_FALLBACK": true, "OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS": true,
		"ASR_MODEL_DIR": true, "TTS_MODEL_DIR": true, "ASR_LANGUAGE": true, "TTS_LANGUAGE": true, "ASR_THREADS": true, "TTS_THREADS": true, "TTS_STEPS": true, "TTS_SPEED": true, "TTS_SID": true,
		"ASR_AUTO_UNLOAD": true, "ASR_IDLE_TIMEOUT_SECONDS": true, "TTS_AUTO_UNLOAD": true, "TTS_IDLE_TIMEOUT_SECONDS": true,
		"VOICE_REPLY_MAX_TOKENS": true, "VOICE_REPLY_TEMPERATURE": true, "VOICE_SYSTEM_PROMPT": true,
		"VOICE_PROFILE_DEFAULT": true,
		"VOICE_GENERIC_SAMPLE_RATE": true, "VOICE_GENERIC_PEAK_TARGET": true, "VOICE_GENERIC_STRIP_EMOJI": true, "VOICE_GENERIC_STREAM_FORMAT": true, "VOICE_GENERIC_CHUNK_MIN_CHARS": true, "VOICE_GENERIC_CHUNK_MAX_CHARS": true,
		"VOICE_M5_SAMPLE_RATE": true, "VOICE_M5_PEAK_TARGET": true, "VOICE_M5_STRIP_EMOJI": true, "VOICE_M5_STREAM_FORMAT": true, "VOICE_M5_CHUNK_MIN_CHARS": true, "VOICE_M5_CHUNK_MAX_CHARS": true,
	}
	return allowed[k]
}

func llmRuntimeKeyV04(k string) bool {
	switch k {
	case "LLM_PROVIDER", "MODEL_PATH", "BACKEND_PORT", "THREADS", "THREADS_BATCH", "CONTEXT", "BATCH", "UBATCH", "PARALLEL", "EXTRA_ARGS":
		return true
	default:
		return false
	}
}

func voiceRuntimeKeyV05(k string) bool {
	switch k {
	case "ASR_MODEL_DIR", "TTS_MODEL_DIR", "ASR_LANGUAGE", "TTS_LANGUAGE", "ASR_THREADS", "TTS_THREADS", "TTS_STEPS", "TTS_SPEED", "TTS_SID",
		"ASR_AUTO_UNLOAD", "ASR_IDLE_TIMEOUT_SECONDS", "TTS_AUTO_UNLOAD", "TTS_IDLE_TIMEOUT_SECONDS":
		return true
	default:
		return false
	}
}

func validateProviderConfig(c config) string {
	if p := strings.ToLower(strings.TrimSpace(c["LLM_PROVIDER"])); p != "local" && p != "openrouter" {
		return "LLM_PROVIDER must be local or openrouter"
	}
	if v := strings.TrimSpace(c["OPENROUTER_PROVIDER_SORT"]); v != "" && v != "price" && v != "throughput" && v != "latency" {
		return "OPENROUTER_PROVIDER_SORT must be blank, price, throughput, or latency"
	}
	if v := strings.TrimSpace(c["OPENROUTER_DATA_COLLECTION"]); v != "" && v != "allow" && v != "deny" {
		return "OPENROUTER_DATA_COLLECTION must be blank, allow, or deny"
	}
	if v := strings.TrimSpace(c["OPENROUTER_VOICE_AUTO_FALLBACK"]); v != "" {
		switch strings.ToLower(v) {
		case "0", "1", "true", "false", "on", "off", "yes", "no":
		default:
			return "OPENROUTER_VOICE_AUTO_FALLBACK must be 0/1 or true/false"
		}
	}
	if v := strings.TrimSpace(c["OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5 {
			return "OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS must be 1..5"
		}
	}
	return ""
}

func (m *manager) handleConfigV04(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, _ := loadConfig(m.configPath)
		writeJSON(w, defaults(c))
	case http.MethodPut:
		var incoming config
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&incoming) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		c, _ := loadConfig(m.configPath)
		c = defaults(c)
		restartLLM := false
		restartVoice := false
		for k, v := range incoming {
			if !configKeyAllowedV04(k) {
				continue
			}
			v = strings.TrimSpace(v)
			if c[k] != v {
				if llmRuntimeKeyV04(k) {
					restartLLM = true
				}
				if voiceRuntimeKeyV05(k) {
					restartVoice = true
				}
			}
			c[k] = v
		}
		if normalizedVoiceProfileName(c["VOICE_PROFILE_DEFAULT"]) == "" {
			http.Error(w, "VOICE_PROFILE_DEFAULT must be generic or m5go", http.StatusBadRequest)
			return
		}
		if msg := validateProviderConfig(c); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := saveConfig(m.configPath, c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if restartLLM {
			_ = m.stopBackend()
		}
		if restartVoice {
			_ = m.stopVoiceWorker()
		}
		writeJSON(w, map[string]any{
			"ok": true, "llm_unloaded": restartLLM, "voice_worker_unloaded": restartVoice,
			"note": "Provider/profile changes apply to the next request. API keys are managed only by /api/openrouter/key. ADMIN_PORT changes require QPKG restart.",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
