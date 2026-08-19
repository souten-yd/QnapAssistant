package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type voiceLLMStreamResult struct {
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

func voiceLLMPayload(cfg config, transcript string, stream bool) map[string]any {
	payload := map[string]any{
		"model": filepath.Base(cfg["MODEL_PATH"]),
		"messages": []map[string]string{
			{"role": "system", "content": get(cfg, "VOICE_SYSTEM_PROMPT", "あなたは音声アシスタントです。日本語で簡潔に答えてください。原則1文、必要な場合でも最大2文。")},
			{"role": "user", "content": strings.TrimSpace(transcript)},
		},
		"max_tokens":  voiceReplyMaxTokens(cfg),
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

func streamVoiceLLM(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, transcript string, llmStart time.Time) (<-chan string, <-chan voiceLLMStreamResult) {
	chunks := make(chan string, 8)
	result := make(chan voiceLLMStreamResult, 1)
	go func() {
		defer close(chunks)
		res := voiceLLMStreamResult{}
		defer func() { result <- res }()

		payload := voiceLLMPayload(cfg, transcript, true)
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

func (m *manager) handleVoiceChatAdaptive(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, "")
	if profile.StreamFormat == "multipart" || r.URL.Query().Get("stream") == "1" {
		m.handleVoiceChatStream(w, r)
		return
	}
	m.handleVoiceChat(w, r)
}

func (m *manager) handleVoiceChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	requestStart := time.Now()
	input, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil || len(input) == 0 {
		http.Error(w, "audio body required", http.StatusBadRequest)
		return
	}
	if err := m.ensureVoiceReady(r.Context()); err != nil {
		http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, "")
	voiceBase := "http://127.0.0.1:" + cfg["VOICE_PORT"]
	stageClient := &http.Client{Timeout: 180 * time.Second}

	asrStart := time.Now()
	ar, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, voiceBase+"/asr", bytes.NewReader(input))
	ar.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if sr := r.Header.Get("X-Sample-Rate"); sr != "" {
		ar.Header.Set("X-Sample-Rate", sr)
	}
	aresp, err := stageClient.Do(ar)
	if err != nil {
		http.Error(w, "ASR failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	asrBody, _ := io.ReadAll(io.LimitReader(aresp.Body, 1<<20))
	aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		http.Error(w, "ASR failed: "+string(asrBody), http.StatusServiceUnavailable)
		return
	}
	var asr voiceChatASR
	asrWall := time.Since(asrStart)
	if json.Unmarshal(asrBody, &asr) != nil || strings.TrimSpace(asr.Text) == "" {
		http.Error(w, "ASR returned no transcript", http.StatusUnprocessableEntity)
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
	if err := writer.WriteEvent(voiceStreamEvent{Type: "transcript", Transcript: asr.Text, Timings: map[string]any{"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF, "ready_ms": time.Since(requestStart).Milliseconds()}}); err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	llmClient := &http.Client{Timeout: 0}
	llmStart := time.Now()
	textChunks, llmResult := streamVoiceLLM(ctx, llmClient, cfg, profile, asr.Text, llmStart)
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
	_ = writer.WriteEvent(voiceStreamEvent{Type: "done", Reply: llm.Reply, Timings: map[string]any{"asr_wall_ms": asrWall.Milliseconds(), "llm_wall_ms": llm.WallMS, "llm_first_token_ms": llm.FirstTokenMS, "llm_predicted_n": llm.PredictedN, "llm_predicted_ms": llm.PredictedMS, "llm_tok_s": llm.TokPerSecond, "first_text_chunk_ms": firstTextMS, "first_audio_ready_ms": firstAudioMS, "stream_chunks": chunkIndex, "finish_reason": llm.FinishReason, "total_ms": time.Since(requestStart).Milliseconds()}})
}
