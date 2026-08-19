package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func configKeyAllowedV04(k string) bool {
	allowed := map[string]bool{
		"MODEL_PATH": true, "MODEL_DIR": true, "MODEL_URL": true, "MODEL_SHA256": true, "MIN_MODEL_BYTES": true,
		"ADMIN_PORT": true, "BACKEND_PORT": true, "THREADS": true, "THREADS_BATCH": true, "CONTEXT": true, "BATCH": true, "UBATCH": true, "PARALLEL": true,
		"IDLE_TIMEOUT_SECONDS": true, "EXTRA_ARGS": true, "VOICE_REPLY_MAX_TOKENS": true, "VOICE_REPLY_TEMPERATURE": true, "VOICE_SYSTEM_PROMPT": true,
		"VOICE_PROFILE_DEFAULT": true,
		"VOICE_GENERIC_SAMPLE_RATE": true, "VOICE_GENERIC_PEAK_TARGET": true, "VOICE_GENERIC_STRIP_EMOJI": true, "VOICE_GENERIC_STREAM_FORMAT": true, "VOICE_GENERIC_CHUNK_MIN_CHARS": true, "VOICE_GENERIC_CHUNK_MAX_CHARS": true,
		"VOICE_M5_SAMPLE_RATE": true, "VOICE_M5_PEAK_TARGET": true, "VOICE_M5_STRIP_EMOJI": true, "VOICE_M5_STREAM_FORMAT": true, "VOICE_M5_CHUNK_MIN_CHARS": true, "VOICE_M5_CHUNK_MAX_CHARS": true,
	}
	return allowed[k]
}

func llmRuntimeKeyV04(k string) bool {
	switch k {
	case "MODEL_PATH", "BACKEND_PORT", "THREADS", "THREADS_BATCH", "CONTEXT", "BATCH", "UBATCH", "PARALLEL", "EXTRA_ARGS":
		return true
	default:
		return false
	}
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
		for k, v := range incoming {
			if !configKeyAllowedV04(k) {
				continue
			}
			v = strings.TrimSpace(v)
			if c[k] != v && llmRuntimeKeyV04(k) {
				restartLLM = true
			}
			c[k] = v
		}
		if normalizedVoiceProfileName(c["VOICE_PROFILE_DEFAULT"]) == "" {
			http.Error(w, "VOICE_PROFILE_DEFAULT must be generic or m5go", http.StatusBadRequest)
			return
		}
		if err := saveConfig(m.configPath, c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if restartLLM {
			_ = m.stopBackend()
		}
		writeJSON(w, map[string]any{"ok": true, "llm_restarted": restartLLM, "note": "Voice profile changes apply to the next request. ADMIN_PORT changes require QPKG restart."})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
