package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Session-aware public handlers. With no session_id they are intentionally the
// same as the 0.4.1 context handlers. A session_id adds only server-managed
// rolling history and compact old-turn memory.

func (m *manager) handleVoiceChatSessionAdaptive(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, "")
	if profile.StreamFormat == "multipart" || r.URL.Query().Get("stream") == "1" {
		m.handleVoiceChatStreamSession(w, r)
		return
	}
	m.handleVoiceChatSession(w, r)
}

func (m *manager) handleVoiceChatSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	input, err := parseVoiceChatInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.ensureVoiceReady(r.Context()); err != nil {
		http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	voiceBase := "http://127.0.0.1:" + cfg["VOICE_PORT"]
	client := &http.Client{Timeout: 180 * time.Second}

	asr, asrWall, err := voiceTranscribe(r.Context(), client, cfg, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	session, controls, sessionBefore, err := beginVoiceSession(r, cfg, input.Controls)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if session != nil {
		defer session.Abort()
	}

	llmStart := time.Now()
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, "LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	llmPayload := voiceLLMPayloadStandard(cfg, asr.Text, false, controls)
	lb, _ := json.Marshal(llmPayload)
	lr, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://127.0.0.1:"+cfg["BACKEND_PORT"]+"/v1/chat/completions", bytes.NewReader(lb))
	lr.Header.Set("Content-Type", "application/json")
	lresp, err := client.Do(lr)
	if err != nil {
		http.Error(w, "LLM request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	lbody, _ := io.ReadAll(io.LimitReader(lresp.Body, 4<<20))
	lresp.Body.Close()
	if lresp.StatusCode != http.StatusOK {
		http.Error(w, "LLM request failed: "+string(lbody), http.StatusBadGateway)
		return
	}
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
		} `json:"usage"`
		Timings struct {
			CacheN             int     `json:"cache_n"`
			PromptN            int     `json:"prompt_n"`
			PromptMS           float64 `json:"prompt_ms"`
			PredictedN         int     `json:"predicted_n"`
			PredictedMS        float64 `json:"predicted_ms"`
			PredictedPerSecond float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if json.Unmarshal(lbody, &decoded) != nil || len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		http.Error(w, "LLM returned no reply", http.StatusBadGateway)
		return
	}
	reply := strings.TrimSpace(decoded.Choices[0].Message.Content)
	llmWall := time.Since(llmStart)

	sessionAfter := sessionBefore
	sessionPersisted := true
	if session != nil {
		var persistErr error
		sessionAfter, persistErr = session.Commit(asr.Text, reply)
		if persistErr != nil {
			sessionPersisted = false
			log.Printf("voice session %s persist failed: %v", sessionBefore.ID, persistErr)
		}
	}

	ttsStart := time.Now()
	ttsPayload, _ := json.Marshal(map[string]any{
		"text": reply,
		"lang": get(cfg, "TTS_LANGUAGE", "ja"),
		"speed": floatVal(cfg, "TTS_SPEED", 1.0),
		"steps": intVal(cfg, "TTS_STEPS", 4),
		"sid": intVal(cfg, "TTS_SID", 0),
	})
	tr, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, voiceBase+"/tts", bytes.NewReader(ttsPayload))
	tr.Header.Set("Content-Type", "application/json")
	tresp, err := client.Do(tr)
	if err != nil {
		http.Error(w, "TTS request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	wav, _ := io.ReadAll(io.LimitReader(tresp.Body, 32<<20))
	tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK {
		http.Error(w, "TTS request failed: "+string(wav), http.StatusServiceUnavailable)
		return
	}
	ttsWall := time.Since(ttsStart)

	timings := map[string]any{
		"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF,
		"llm_wall_ms": llmWall.Milliseconds(),
		"llm_cache_n": decoded.Timings.CacheN, "llm_prompt_n": decoded.Timings.PromptN, "llm_prompt_ms": decoded.Timings.PromptMS,
		"llm_predicted_n": decoded.Timings.PredictedN, "llm_predicted_ms": decoded.Timings.PredictedMS, "llm_tok_s": decoded.Timings.PredictedPerSecond,
		"llm_prompt_tokens": decoded.Usage.PromptTokens, "llm_completion_tokens": decoded.Usage.CompletionTokens,
		"llm_finish_reason": decoded.Choices[0].FinishReason,
		"llm_max_tokens": voiceMaxTokensTelemetry(cfg, controls), "llm_history_messages": len(controls.History),
		"tts_wall_ms": ttsWall.Milliseconds(), "tts_engine_ms": tresp.Header.Get("X-Qnap-Processing-Ms"), "tts_rtf": tresp.Header.Get("X-Qnap-RTF"),
	}
	addVoiceSessionTimings(timings, sessionAfter)
	if sessionAfter.ID != "" {
		timings["session_persisted"] = sessionPersisted
	}
	writeJSON(w, voiceChatResponse{Transcript: asr.Text, Reply: reply, Audio: base64.StdEncoding.EncodeToString(wav), AudioType: "audio/wav", Timings: timings})
}

func (m *manager) handleVoiceChatStreamSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	requestStart := time.Now()
	input, err := parseVoiceChatInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.ensureVoiceReady(r.Context()); err != nil {
		http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, "")
	stageClient := &http.Client{Timeout: 180 * time.Second}

	asr, asrWall, err := voiceTranscribe(r.Context(), stageClient, cfg, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	session, controls, sessionBefore, err := beginVoiceSession(r, cfg, input.Controls)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if session != nil {
		defer session.Abort()
	}

	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, "LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported by HTTP server", http.StatusInternalServerError)
		return
	}
	writer := newVoiceStreamWriter(w, flusher, profile)
	defer writer.Close()
	transcriptTimings := map[string]any{
		"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF,
		"ready_ms": time.Since(requestStart).Milliseconds(), "llm_max_tokens": voiceMaxTokensTelemetry(cfg, controls),
		"llm_history_messages": len(controls.History),
	}
	addVoiceSessionTimings(transcriptTimings, sessionBefore)
	if err := writer.WriteEvent(voiceStreamEvent{Type: "transcript", Transcript: asr.Text, Timings: transcriptTimings}); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	llmClient := &http.Client{Timeout: 0}
	llmStart := time.Now()
	textChunks, llmResult := streamVoiceLLMStandard(ctx, llmClient, cfg, profile, asr.Text, controls, llmStart)
	firstTextMS := int64(0)
	firstAudioMS := int64(0)
	chunkIndex := 0
	for text := range textChunks {
		textReadyMS := time.Since(requestStart).Milliseconds()
		if firstTextMS == 0 {
			firstTextMS = textReadyMS
		}
		if err := writer.WriteEvent(voiceStreamEvent{Type: "text", Index: chunkIndex, Text: text, Timings: map[string]any{"ready_ms": textReadyMS}}); err != nil {
			cancel()
			return
		}
		req := publicSpeechRequest{Text: text, Lang: get(cfg, "TTS_LANGUAGE", "ja"), Speed: floatVal(cfg, "TTS_SPEED", 1.0), Steps: intVal(cfg, "TTS_STEPS", 4), Sid: intVal(cfg, "TTS_SID", 0)}
		audio, worker, wallMS, err := synthesizeProfileSpeech(ctx, stageClient, cfg, profile, req)
		if err != nil {
			_ = writer.WriteEvent(voiceStreamEvent{Type: "error", Error: err.Error()})
			cancel()
			return
		}
		audioReadyMS := time.Since(requestStart).Milliseconds()
		if firstAudioMS == 0 {
			firstAudioMS = audioReadyMS
		}
		timings := map[string]any{"tts_wall_ms": wallMS, "tts_engine_ms": worker.EngineMS, "tts_rtf": worker.RTF, "ready_ms": audioReadyMS}
		if err := writer.WriteAudio(chunkIndex, text, audio, worker.Backend, timings); err != nil {
			cancel()
			return
		}
		chunkIndex++
	}
	llm := <-llmResult
	if llm.Err != nil {
		_ = writer.WriteEvent(voiceStreamEvent{Type: "error", Error: llm.Err.Error()})
		return
	}

	sessionAfter := sessionBefore
	sessionPersisted := true
	if session != nil {
		var persistErr error
		sessionAfter, persistErr = session.Commit(asr.Text, llm.Reply)
		if persistErr != nil {
			sessionPersisted = false
			log.Printf("voice session %s persist failed: %v", sessionBefore.ID, persistErr)
		}
	}

	doneTimings := map[string]any{
		"asr_wall_ms": asrWall.Milliseconds(), "llm_wall_ms": llm.WallMS, "llm_first_token_ms": llm.FirstTokenMS,
		"llm_predicted_n": llm.PredictedN, "llm_predicted_ms": llm.PredictedMS, "llm_tok_s": llm.TokPerSecond,
		"llm_max_tokens": voiceMaxTokensTelemetry(cfg, controls), "llm_history_messages": len(controls.History),
		"first_text_chunk_ms": firstTextMS, "first_audio_ready_ms": firstAudioMS, "stream_chunks": chunkIndex,
		"finish_reason": llm.FinishReason, "total_ms": time.Since(requestStart).Milliseconds(),
	}
	addVoiceSessionTimings(doneTimings, sessionAfter)
	if sessionAfter.ID != "" {
		doneTimings["session_persisted"] = sessionPersisted
	}
	_ = writer.WriteEvent(voiceStreamEvent{Type: "done", Reply: llm.Reply, Timings: doneTimings})
}
