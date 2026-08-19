package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type publicSpeechRequest struct {
	Text       string   `json:"text"`
	Lang       string   `json:"lang,omitempty"`
	Backend    string   `json:"backend,omitempty"`
	Speed      float64  `json:"speed,omitempty"`
	Steps      int      `json:"steps,omitempty"`
	Sid        int      `json:"sid,omitempty"`
	SampleRate *int     `json:"sample_rate,omitempty"`
	Peak       *float64 `json:"peak,omitempty"`
	Profile    string   `json:"profile,omitempty"`
}

type workerTTSResponse struct {
	WAV      []byte
	Backend  string
	EngineMS string
	RTF      string
}

func requestWorkerTTS(ctx context.Context, client *http.Client, cfg config, req publicSpeechRequest) (workerTTSResponse, error) {
	voiceBase := "http://127.0.0.1:" + cfg["VOICE_PORT"]
	payload, _ := json.Marshal(map[string]any{"text": req.Text, "lang": req.Lang, "backend": req.Backend, "speed": req.Speed, "steps": req.Steps, "sid": req.Sid})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, voiceBase+"/tts", bytes.NewReader(payload))
	if err != nil {
		return workerTTSResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return workerTTSResponse{}, err
	}
	defer resp.Body.Close()
	wav, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return workerTTSResponse{}, fmt.Errorf("TTS failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(wav)))
	}
	return workerTTSResponse{WAV: wav, Backend: resp.Header.Get("X-Qnap-TTS-Backend"), EngineMS: resp.Header.Get("X-Qnap-Processing-Ms"), RTF: resp.Header.Get("X-Qnap-RTF")}, nil
}

func synthesizeProfileSpeech(ctx context.Context, client *http.Client, cfg config, profile voiceClientProfile, req publicSpeechRequest) (processedVoiceAudio, workerTTSResponse, int64, error) {
	req.Text = profileText(profile, req.Text)
	if strings.TrimSpace(req.Text) == "" {
		return processedVoiceAudio{}, workerTTSResponse{}, 0, fmt.Errorf("text is empty after profile filtering")
	}
	started := time.Now()
	worker, err := requestWorkerTTS(ctx, client, cfg, req)
	if err != nil {
		return processedVoiceAudio{}, workerTTSResponse{}, 0, err
	}
	targetRate := intPointerValue(req.SampleRate, profile.SampleRate)
	peakTarget := floatPointerValue(req.Peak, profile.PeakTarget)
	audio, err := processVoiceWAV(worker.WAV, targetRate, peakTarget)
	return audio, worker, time.Since(started).Milliseconds(), err
}

func setProfileAudioHeaders(w http.ResponseWriter, profile voiceClientProfile, audio processedVoiceAudio, worker workerTTSResponse, wallMS int64) {
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-Qnap-Voice-Profile", profile.Name)
	w.Header().Set("X-Qnap-Sample-Rate", strconv.Itoa(audio.SampleRate))
	w.Header().Set("X-Qnap-Source-Sample-Rate", strconv.Itoa(audio.SourceSampleRate))
	w.Header().Set("X-Qnap-Peak", fmt.Sprintf("%.6f", audio.Peak))
	w.Header().Set("X-Qnap-Audio-Seconds", fmt.Sprintf("%.3f", audio.DurationSeconds))
	w.Header().Set("X-Qnap-TTS-Backend", worker.Backend)
	w.Header().Set("X-Qnap-Engine-Processing-Ms", worker.EngineMS)
	w.Header().Set("X-Qnap-Engine-RTF", worker.RTF)
	w.Header().Set("X-Qnap-Processing-Ms", strconv.FormatInt(wallMS, 10))
	if audio.DurationSeconds > 0 {
		w.Header().Set("X-Qnap-RTF", fmt.Sprintf("%.4f", float64(wallMS)/1000.0/audio.DurationSeconds))
	}
}

func (m *manager) handleVoiceSpeech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := m.ensureVoiceReady(r.Context()); err != nil {
		http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req publicSpeechRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, req.Profile)
	if req.Lang == "" {
		req.Lang = get(cfg, "TTS_LANGUAGE", "ja")
	}
	client := &http.Client{Timeout: 180 * time.Second}
	audio, worker, wallMS, err := synthesizeProfileSpeech(r.Context(), client, cfg, profile, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	setProfileAudioHeaders(w, profile, audio, worker, wallMS)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.WAV)
}

func splitVoiceText(text string, minChars, maxChars int) []string {
	pending := strings.TrimSpace(text)
	out := []string{}
	for pending != "" {
		chunk, rest, ok := takeVoiceChunk(pending, minChars, maxChars)
		if !ok {
			chunk = strings.TrimSpace(pending)
			rest = ""
		}
		if chunk != "" {
			out = append(out, chunk)
		}
		pending = strings.TrimSpace(rest)
	}
	return out
}

func (m *manager) handleVoiceSpeechStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := m.ensureVoiceReady(r.Context()); err != nil {
		http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req publicSpeechRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	profile := requestedVoiceProfile(r, cfg, req.Profile)
	text := profileText(profile, req.Text)
	if text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if req.Lang == "" {
		req.Lang = get(cfg, "TTS_LANGUAGE", "ja")
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported by HTTP server", http.StatusInternalServerError)
		return
	}
	writer := newVoiceStreamWriter(w, flusher, profile)
	defer writer.Close()
	start := time.Now()
	chunks := splitVoiceText(text, profile.ChunkMinChars, profile.ChunkMaxChars)
	_ = writer.WriteEvent(voiceStreamEvent{Type: "meta", Timings: map[string]any{"chunks": len(chunks), "ready_ms": 0}})
	client := &http.Client{Timeout: 180 * time.Second}
	for i, chunk := range chunks {
		chunkReq := req
		chunkReq.Text = chunk
		audio, worker, wallMS, err := synthesizeProfileSpeech(r.Context(), client, cfg, profile, chunkReq)
		if err != nil {
			_ = writer.WriteEvent(voiceStreamEvent{Type: "error", Error: err.Error()})
			return
		}
		timings := map[string]any{"tts_wall_ms": wallMS, "tts_engine_ms": worker.EngineMS, "tts_rtf": worker.RTF, "ready_ms": time.Since(start).Milliseconds()}
		_ = writer.WriteEvent(voiceStreamEvent{Type: "text", Index: i, Text: chunk, Timings: timings})
		if err := writer.WriteAudio(i, chunk, audio, worker.Backend, timings); err != nil {
			return
		}
	}
	_ = writer.WriteEvent(voiceStreamEvent{Type: "done", Reply: text, Timings: map[string]any{"total_ms": time.Since(start).Milliseconds(), "stream_chunks": len(chunks)}})
}
