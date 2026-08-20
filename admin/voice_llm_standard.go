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

// voiceConfiguredMaxTokens returns false when the request and persistent
// configuration both leave the reply limit unspecified. In that case the
// `max_tokens` key is omitted entirely and llama.cpp/OpenAI-compatible backend
// semantics decide how long the completion may be. This deliberately keeps
// reply length separate from M5 audio chunking: a long completion can still be
// emitted as many 8-18 character TTS chunks.
func voiceConfiguredMaxTokens(cfg config, controls voiceChatControls) (int, bool) {
	if controls.MaxTokens != nil {
		return *controls.MaxTokens, true
	}
	n := intVal(cfg, "VOICE_REPLY_MAX_TOKENS", 0)
	if n <= 0 {
		return 0, false
	}
	if n > voiceMaxReplyTokens {
		n = voiceMaxReplyTokens
	}
	return n, true
}

func voiceLLMPayloadStandard(cfg config, transcript string, stream bool, controls voiceChatControls) map[string]any {
	payload := map[string]any{
		"model":       filepath.Base(cfg["MODEL_PATH"]),
		"messages":    voiceLLMMessages(cfg, transcript, controls),
		"temperature": voiceReplyTemperature(cfg),
		"stream":      stream,
	}
	if n, ok := voiceConfiguredMaxTokens(cfg, controls); ok {
		payload["max_tokens"] = n
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

func streamVoiceLLMStandard(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, transcript string, controls voiceChatControls, llmStart time.Time) (<-chan string, <-chan voiceLLMContextStreamResult) {
	chunks := make(chan string, 8)
	result := make(chan voiceLLMContextStreamResult, 1)
	go func() {
		defer close(chunks)
		res := voiceLLMContextStreamResult{}
		defer func() { result <- res }()

		payload := voiceLLMPayloadStandard(cfg, transcript, true, controls)
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

func voiceMaxTokensTelemetry(cfg config, controls voiceChatControls) any {
	if n, ok := voiceConfiguredMaxTokens(cfg, controls); ok {
		return n
	}
	return "backend_default"
}
