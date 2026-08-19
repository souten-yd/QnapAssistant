package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	signal.Ignore(syscall.SIGHUP)
	cfg := loadEngineConfig()
	e := &engine{cfg: cfg}
	defer e.close()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "service": "QnapAssistant Voice Worker"})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"ok":                       true,
			"asr_model_ready":          e.asrFilesReady(),
			"supertonic_model_ready":   e.supertonicFilesReady(),
			"piper_runtime_ready":      e.piperRuntimeReady(),
			"piper_model_ready":        e.piperModelReady(),
			"piper_openjtalk_ready":    e.piperOpenJTalkReady(),
			"piper_compat_ready":       e.piperCompatReady(),
			"asr_loaded":               e.asr != nil,
			"tts_loaded":               e.tts != nil,
			"asr_threads":              cfg.ASRThreads,
			"tts_threads":              cfg.TTSThreads,
			"tts_steps":                cfg.TTSSteps,
			"tts_backend":              cfg.TTSBackend,
			"tts_fallback_backend":     cfg.TTSFallbackBackend,
			"asr_model_dir":            cfg.ASRModelDir,
			"tts_model_dir":            cfg.TTSModelDir,
			"piper_runtime_dir":        cfg.PiperRuntimeDir,
			"piper_model_dir":          cfg.PiperModelDir,
			"piper_openjtalk_directory": e.piperOpenJTalkDir(),
		})
	})
	mux.HandleFunc("/asr", e.handleASR)
	mux.HandleFunc("/tts", e.handleTTS)

	port := getenv("VOICE_PORT", "11437")
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("voice worker listening on 127.0.0.1:%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("voice worker stopped: %v", err)
			stop <- syscall.SIGTERM
		}
	}()
	<-stop
	ctx, cancel := signalContext(5 * time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (e *engine) handleASR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var a pcmAudio
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "audio/wav") || strings.Contains(ct, "audio/x-wav") {
		a, err = decodeWAV(body)
	} else {
		sr, _ := strconv.Atoi(r.Header.Get("X-Sample-Rate"))
		a, err = decodePCM16(body, sr)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := e.recognize(a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, result)
}

func (e *engine) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req ttsOptions
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := e.synthesize(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-Qnap-TTS-Backend", result.Backend)
	w.Header().Set("X-Qnap-Sample-Rate", strconv.Itoa(result.SampleRate))
	w.Header().Set("X-Qnap-Audio-Seconds", fmt.Sprintf("%.3f", result.AudioSec))
	w.Header().Set("X-Qnap-Processing-Ms", strconv.FormatInt(result.ProcessMS, 10))
	w.Header().Set("X-Qnap-RTF", fmt.Sprintf("%.4f", result.RTF))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.WAV)
}

func loadEngineConfig() engineConfig {
	voiceDir := getenv("VOICE_DIR", "/share/Public/QnapAssistant/voice")
	return engineConfig{
		VoiceDir:           voiceDir,
		ASRModelDir:        getenv("ASR_MODEL_DIR", filepath.Join(voiceDir, "sensevoice")),
		TTSModelDir:        getenv("TTS_MODEL_DIR", filepath.Join(voiceDir, "supertonic3")),
		ASRLanguage:        getenv("ASR_LANGUAGE", "ja"),
		TTSLanguage:        getenv("TTS_LANGUAGE", "ja"),
		ASRThreads:         envInt("ASR_THREADS", 4),
		TTSThreads:         envInt("TTS_THREADS", 2),
		TTSSteps:           envInt("TTS_STEPS", 4),
		TTSSpeed:           envFloat("TTS_SPEED", 1.0),
		TTSSid:             envInt("TTS_SID", 0),
		TTSBackend:         getenv("TTS_BACKEND", "piper_plus"),
		TTSFallbackBackend: getenv("TTS_FALLBACK_BACKEND", "supertonic"),
		PiperRuntimeDir:    getenv("PIPER_RUNTIME_DIR", filepath.Join(voiceDir, "piper-plus-runtime")),
		PiperModelDir:      getenv("PIPER_MODEL_DIR", filepath.Join(voiceDir, "piper-plus-tsukuyomi")),
		PiperModelFile:     getenv("PIPER_MODEL_FILE", "tsukuyomi-chan-6lang-fp16.onnx"),
		PiperConfigFile:    getenv("PIPER_CONFIG_FILE", "config.json"),
		PiperNoiseScale:    envFloat("PIPER_NOISE_SCALE", 0.5),
		PiperLengthScale:   envFloat("PIPER_LENGTH_SCALE", 1.0),
	}
}

func getenv(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	n, err := strconv.Atoi(getenv(k, ""))
	if err != nil {
		return d
	}
	return n
}
func envFloat(k string, d float32) float32 {
	v, err := strconv.ParseFloat(getenv(k, ""), 32)
	if err != nil {
		return d
	}
	return float32(v)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func signalContext(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
