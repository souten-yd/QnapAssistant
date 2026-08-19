package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type voiceProcessState struct {
	mu      sync.Mutex
	startMu sync.Mutex
	cmd     *exec.Cmd
	started time.Time
}

var voiceProcess voiceProcessState

func voiceRunningLocked() bool { return voiceProcess.cmd != nil && voiceProcess.cmd.Process != nil }

func (m *manager) startVoiceWorker() error {
	voiceProcess.startMu.Lock()
	defer voiceProcess.startMu.Unlock()
	voiceProcess.mu.Lock()
	if voiceRunningLocked() {
		voiceProcess.mu.Unlock()
		return nil
	}
	voiceProcess.mu.Unlock()

	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	bin := filepath.Join(m.qpkgDir, "bin", "qnap-voice-worker")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("voice worker unavailable: %w", err)
	}
	logPath := filepath.Join(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), "voice-worker.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"VOICE_PORT="+get(cfg, "VOICE_PORT", "11437"),
		"VOICE_DIR="+get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"),
		"ASR_MODEL_DIR="+get(cfg, "ASR_MODEL_DIR", "/share/Public/QnapAssistant/voice/sensevoice"),
		"TTS_MODEL_DIR="+get(cfg, "TTS_MODEL_DIR", "/share/Public/QnapAssistant/voice/supertonic3"),
		"ASR_LANGUAGE="+get(cfg, "ASR_LANGUAGE", "ja"),
		"TTS_LANGUAGE="+get(cfg, "TTS_LANGUAGE", "ja"),
		"ASR_THREADS="+get(cfg, "ASR_THREADS", "4"),
		"TTS_THREADS="+get(cfg, "TTS_THREADS", "2"),
		"TTS_STEPS="+get(cfg, "TTS_STEPS", "8"),
		"TTS_SPEED="+get(cfg, "TTS_SPEED", "1.0"),
		"TTS_SID="+get(cfg, "TTS_SID", "0"),
	)
	cmd.Stdin = nil
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	voiceProcess.mu.Lock()
	voiceProcess.cmd = cmd
	voiceProcess.started = time.Now()
	voiceProcess.mu.Unlock()
	go func(c *exec.Cmd, f *os.File) {
		err := c.Wait()
		f.Close()
		voiceProcess.mu.Lock()
		if voiceProcess.cmd == c {
			voiceProcess.cmd = nil
		}
		voiceProcess.mu.Unlock()
		if err != nil {
			log.Printf("voice worker exited: %v", err)
		}
	}(cmd, lf)
	return nil
}

func (m *manager) ensureVoiceReady(ctx context.Context) error {
	if err := m.startVoiceWorker(); err != nil {
		return err
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	health := "http://127.0.0.1:" + get(cfg, "VOICE_PORT", "11437") + "/health"
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Get(health)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
			voiceProcess.mu.Lock()
			running := voiceRunningLocked()
			voiceProcess.mu.Unlock()
			if !running {
				return errors.New("voice worker exited before becoming ready")
			}
		}
	}
}

func (m *manager) stopVoiceWorker() error {
	voiceProcess.mu.Lock()
	cmd := voiceProcess.cmd
	if cmd == nil || cmd.Process == nil {
		voiceProcess.mu.Unlock()
		return nil
	}
	pid := cmd.Process.Pid
	voiceProcess.cmd = nil
	voiceProcess.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return nil
}

func (m *manager) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	voiceProcess.mu.Lock()
	running := voiceRunningLocked()
	started := voiceProcess.started
	voiceProcess.mu.Unlock()
	if !running {
		writeJSON(w, map[string]any{"worker_running": false})
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	resp, err := http.Get("http://127.0.0.1:" + cfg["VOICE_PORT"] + "/status")
	if err != nil {
		writeJSON(w, map[string]any{"worker_running": true, "worker_status_error": err.Error()})
		return
	}
	defer resp.Body.Close()
	var status map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&status)
	status["worker_running"] = true
	status["worker_uptime_seconds"] = int64(time.Since(started).Seconds())
	writeJSON(w, status)
}

func (m *manager) handleVoiceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/voice/")
	var err error
	switch action {
	case "start":
		err = m.ensureVoiceReady(r.Context())
	case "stop":
		err = m.stopVoiceWorker()
	case "restart":
		_ = m.stopVoiceWorker()
		err = m.ensureVoiceReady(r.Context())
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (m *manager) handleVoiceProxy(workerPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := m.ensureVoiceReady(r.Context()); err != nil {
			http.Error(w, "voice worker startup failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		target, _ := url.Parse("http://127.0.0.1:" + cfg["VOICE_PORT"])
		p := httputil.NewSingleHostReverseProxy(target)
		original := r.URL.Path
		r.URL.Path = workerPath
		p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) { http.Error(w, err.Error(), http.StatusBadGateway) }
		p.ServeHTTP(w, r)
		r.URL.Path = original
	}
}

type voiceChatASR struct {
	Text      string  `json:"text"`
	AudioSec  float64 `json:"audio_seconds"`
	ProcessMS int64   `json:"processing_ms"`
	RTF       float64 `json:"rtf"`
}

type voiceChatResponse struct {
	Transcript string         `json:"transcript"`
	Reply      string         `json:"reply"`
	Audio      string         `json:"audio_base64"`
	AudioType  string         `json:"audio_type"`
	Timings    map[string]any `json:"timings"`
}

func (m *manager) handleVoiceChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
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
	voiceBase := "http://127.0.0.1:" + cfg["VOICE_PORT"]
	client := &http.Client{Timeout: 180 * time.Second}

	asrStart := time.Now()
	ar, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, voiceBase+"/asr", bytes.NewReader(input))
	ar.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if sr := r.Header.Get("X-Sample-Rate"); sr != "" {
		ar.Header.Set("X-Sample-Rate", sr)
	}
	aresp, err := client.Do(ar)
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

	llmStart := time.Now()
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, "LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	prompt := strings.TrimSpace(asr.Text)
	if strings.Contains(strings.ToLower(filepath.Base(cfg["MODEL_PATH"])), "qwen3") {
		if mode, ok := normalizeThinkingMode(cfg["THINKING_MODE"]); ok {
			if mode == "off" {
				prompt += " /no_think"
			} else if mode == "on" {
				prompt += " /think"
			}
		}
	}
	maxTokens := intVal(cfg, "VOICE_MAX_TOKENS", 128)
	llmPayload := map[string]any{"model": filepath.Base(cfg["MODEL_PATH"]), "messages": []map[string]string{{"role": "user", "content": prompt}}, "max_tokens": maxTokens}
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
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(lbody, &decoded) != nil || len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		http.Error(w, "LLM returned no reply", http.StatusBadGateway)
		return
	}
	reply := strings.TrimSpace(decoded.Choices[0].Message.Content)
	llmWall := time.Since(llmStart)

	ttsStart := time.Now()
	ttsPayload, _ := json.Marshal(map[string]any{"text": reply, "lang": get(cfg, "TTS_LANGUAGE", "ja"), "speed": floatVal(cfg, "TTS_SPEED", 1.0), "steps": intVal(cfg, "TTS_STEPS", 8), "sid": intVal(cfg, "TTS_SID", 0)})
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
		Transcript: asr.Text, Reply: reply, Audio: base64.StdEncoding.EncodeToString(wav), AudioType: "audio/wav",
		Timings: map[string]any{
			"asr_wall_ms": asrWall.Milliseconds(), "asr_engine_ms": asr.ProcessMS, "asr_rtf": asr.RTF,
			"llm_wall_ms": llmWall.Milliseconds(), "tts_wall_ms": ttsWall.Milliseconds(),
			"tts_engine_ms": tresp.Header.Get("X-Qnap-Processing-Ms"), "tts_rtf": tresp.Header.Get("X-Qnap-RTF"),
		},
	})
}

func floatVal(c config, k string, d float64) float64 {
	v, err := strconv.ParseFloat(get(c, k, ""), 64)
	if err != nil {
		return d
	}
	return v
}
