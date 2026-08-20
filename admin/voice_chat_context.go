package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Context-aware voice chat entry points. The 0.4 handlers remain in place for
// source compatibility; main.go routes public voice-chat requests here.
// Existing audio-only requests produce exactly the same default LLM payload.

func voiceLLMPayloadWithControls(cfg config, transcript string, stream bool, controls voiceChatControls) map[string]any {
	payload := map[string]any{
		"model":       filepath.Base(cfg["MODEL_PATH"]),
		"messages":    voiceLLMMessages(cfg, transcript, controls),
		"max_tokens":  voiceEffectiveMaxTokens(cfg, controls),
		"temperature": voiceReplyTemperature(cfg),
		"stream":      stream,
	}
	if strings.Contains(strings.ToLower(filepath.Base(cfg["MODEL_PATH"])), "qwen3") {
		if mode, ok := normalizeThinkingMode(cfg["THINKING_MODE"]); ok {
			switch mode {
			case "off":
				payload["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
				payload["reasoning_effort"] = "none"
			case "on":
				payload["chat_template_kwargs"] = map[string]any{"enable_thinking": true}
			}
		}
	}
	return payload
}

func voiceTranscribe(ctx context.Context, client *http.Client, cfg config, input voiceChatInput) (voiceChatASR, time.Duration, error) {
	voiceBase := "http://127.0.0.1:" + cfg["VOICE_PORT"]
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, voiceBase+"/asr", bytes.NewReader(input.Audio))
	if err != nil {
		return voiceChatASR{}, 0, err
	}
	req.Header.Set("Content-Type", input.AudioContentType)
	if input.SampleRate != "" {
		req.Header.Set("X-Sample-Rate", input.SampleRate)
	}
	resp, err := client.Do(req)
	if err != nil {
		return voiceChatASR{}, 0, fmt.Errorf("ASR failed: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	wall := time.Since(started)
	if resp.StatusCode != http.StatusOK {
		return voiceChatASR{}, wall, fmt.Errorf("ASR failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var asr voiceChatASR
	if json.Unmarshal(body, &asr) != nil || strings.TrimSpace(asr.Text) == "" {
		return voiceChatASR{}, wall, fmt.Errorf("ASR returned no transcript")
	}
	return asr, wall, nil
}

func (m *manager) handleVoiceChatContextAdaptive(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, "")
	if profile.StreamFormat == "multipart" || r.URL.Query().Get("stream") == "1" {
		m.handleVoiceChatStreamContext(w, r)
		return
	}
	m.handleVoiceChatContext(w, r)
}

func (m *manager) handleVoiceChatContext(w http.ResponseWriter, r *http.Request) {
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

	llmStart := time.Now()
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, "LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	llmPayload := voiceLLMPayloadWithControls(cfg, asr.Text, false, input.Controls)
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
	writeJSON(w, voiceChatResponse{
		Transcript: asr.Text,
		Reply:      reply,
		Audio:      base64.StdEncoding.EncodeToString(wav),
		AudioType:  "audio/wav",
		Timings: map[string]any{
			"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF,
			"llm_wall_ms": llmWall.Milliseconds(),
			"llm_cache_n": decoded.Timings.CacheN, "llm_prompt_n": decoded.Timings.PromptN, "llm_prompt_ms": decoded.Timings.PromptMS,
			"llm_predicted_n": decoded.Timings.PredictedN, "llm_predicted_ms": decoded.Timings.PredictedMS, "llm_tok_s": decoded.Timings.PredictedPerSecond,
			"llm_prompt_tokens": decoded.Usage.PromptTokens, "llm_completion_tokens": decoded.Usage.CompletionTokens,
			"llm_finish_reason": decoded.Choices[0].FinishReason,
			"llm_max_tokens": voiceEffectiveMaxTokens(cfg, input.Controls), "llm_history_messages": len(input.Controls.History),
			"tts_wall_ms": ttsWall.Milliseconds(), "tts_engine_ms": tresp.Header.Get("X-Qnap-Processing-Ms"), "tts_rtf": tresp.Header.Get("X-Qnap-RTF"),
		},
	})
}

type voiceLLMContextStreamResult struct {
	Reply          string
	FinishReason   string
	FirstTokenMS   int64
	WallMS         int64
	StreamChunkCnt int
	PredictedN     int
	PredictedMS    float64
	TokPerSecond   float64
	Err            error
}

func streamVoiceLLMWithControls(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, transcript string, controls voiceChatControls, llmStart time.Time) (<-chan string, <-chan voiceLLMContextStreamResult) {
	chunks := make(chan string, 8)
	result := make(chan voiceLLMContextStreamResult, 1)
	go func() {
		defer close(chunks)
		res := voiceLLMContextStreamResult{}
		defer func() { result <- res }()

		payload := voiceLLMPayloadWithControls(cfg, transcript, true, controls)
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+cfg["BACKEND_PORT"]+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			res.Err = err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			res.Err = fmt.Errorf("LLM stream request failed: %w", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			res.Err = fmt.Errorf("LLM stream request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			return
		}

		var full strings.Builder
		pending := ""
		firstTokenSeen := false
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var packet struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
				Timings struct {
					PredictedN         int     `json:"predicted_n"`
					PredictedMS        float64 `json:"predicted_ms"`
					PredictedPerSecond float64 `json:"predicted_per_second"`
				} `json:"timings"`
			}
			if err := json.Unmarshal([]byte(data), &packet); err != nil {
				continue
			}
			if packet.Timings.PredictedN > 0 {
				res.PredictedN = packet.Timings.PredictedN
				res.PredictedMS = packet.Timings.PredictedMS
				res.TokPerSecond = packet.Timings.PredictedPerSecond
			}
			if len(packet.Choices) == 0 {
				continue
			}
			choice := packet.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				res.FinishReason = *choice.FinishReason
			}
			delta := choice.Delta.Content
			if delta == "" {
				continue
			}
			if !firstTokenSeen {
				res.FirstTokenMS = time.Since(llmStart).Milliseconds()
				firstTokenSeen = true
			}
			full.WriteString(delta)
			pending += delta
			for {
				chunk, rest, ok := takeVoiceChunk(pending, profile.ChunkMinChars, profile.ChunkMaxChars)
				if !ok {
					break
				}
				pending = rest
				chunk = profileText(profile, chunk)
				if chunk == "" {
					continue
				}
				res.StreamChunkCnt++
				select {
				case chunks <- chunk:
				case <-ctx.Done():
					res.Err = ctx.Err()
					return
				}
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			res.Err = fmt.Errorf("read LLM stream: %w", err)
			return
		}
		if tail := profileText(profile, pending); tail != "" {
			res.StreamChunkCnt++
			select {
			case chunks <- tail:
			case <-ctx.Done():
				res.Err = ctx.Err()
				return
			}
		}
		res.Reply = profileText(profile, full.String())
		res.WallMS = time.Since(llmStart).Milliseconds()
		if res.Reply == "" && res.Err == nil {
			res.Err = fmt.Errorf("LLM stream returned no reply")
		}
	}()
	return chunks, result
}

func (m *manager) handleVoiceChatStreamContext(w http.ResponseWriter, r *http.Request) {
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
	if err := writer.WriteEvent(voiceStreamEvent{Type: "transcript", Transcript: asr.Text, Timings: map[string]any{
		"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF,
		"ready_ms": time.Since(requestStart).Milliseconds(), "llm_max_tokens": voiceEffectiveMaxTokens(cfg, input.Controls),
		"llm_history_messages": len(input.Controls.History),
	}}); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	llmClient := &http.Client{Timeout: 0}
	llmStart := time.Now()
	textChunks, llmResult := streamVoiceLLMWithControls(ctx, llmClient, cfg, profile, asr.Text, input.Controls, llmStart)
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
	_ = writer.WriteEvent(voiceStreamEvent{Type: "done", Reply: llm.Reply, Timings: map[string]any{
		"asr_wall_ms": asrWall.Milliseconds(), "llm_wall_ms": llm.WallMS, "llm_first_token_ms": llm.FirstTokenMS,
		"llm_predicted_n": llm.PredictedN, "llm_predicted_ms": llm.PredictedMS, "llm_tok_s": llm.TokPerSecond,
		"llm_max_tokens": voiceEffectiveMaxTokens(cfg, input.Controls), "llm_history_messages": len(input.Controls.History),
		"first_text_chunk_ms": firstTextMS, "first_audio_ready_ms": firstAudioMS, "stream_chunks": chunkIndex,
		"finish_reason": llm.FinishReason, "total_ms": time.Since(requestStart).Milliseconds(),
	}})
}
