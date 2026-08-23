package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const defaultConfigPath = "/share/Public/QnapAssistant/config.env"

func main() {
	signal.Ignore(syscall.SIGHUP)
	qpkgDir := os.Getenv("QPKG_DIR")
	if qpkgDir == "" {
		exe, _ := os.Executable()
		qpkgDir = filepath.Dir(filepath.Dir(exe))
	}
	configPath := os.Getenv("QNAP_ASSISTANT_CONFIG")
	if configPath == "" {
		configPath = defaultConfigPath
	}
	m := &manager{qpkgDir: qpkgDir, configPath: configPath, lastUsed: time.Now()}
	go m.idleLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", m.handleHealth)
	mux.HandleFunc("/api/status", m.handleStatus)
	mux.HandleFunc("/api/bootstrap", m.handleBootstrapStatus)
	mux.HandleFunc("/api/config", m.handleConfigV04)
	mux.HandleFunc("/api/thinking", m.handleThinking)
	mux.HandleFunc("/api/logs", m.handleLogs)
	mux.HandleFunc("/api/models", m.handleModels)
	mux.HandleFunc("/api/models/select", m.handleModelSelect)
	mux.HandleFunc("/api/models/download", m.handleModelDownload)
	mux.HandleFunc("/api/openrouter/key", m.handleOpenRouterKey)
	mux.HandleFunc("/api/openrouter/check", m.handleOpenRouterCheck)
	mux.HandleFunc("/api/openrouter/models", m.handleOpenRouterModels)
	mux.HandleFunc("/api/openrouter/test", m.handleOpenRouterTest)
	mux.HandleFunc("/api/llm/start", m.handleLLMStart)
	mux.HandleFunc("/api/llm/stop", m.handleLLMStop)
	mux.HandleFunc("/api/llm/restart", m.handleLLMRestart)
	mux.HandleFunc("/api/voice/status", m.handleVoiceStatus)
	mux.HandleFunc("/api/voice/protocol", m.handleVoiceProtocol)
	mux.HandleFunc("/api/voice/start", m.handleVoiceAction)
	mux.HandleFunc("/api/voice/stop", m.handleVoiceAction)
	mux.HandleFunc("/api/voice/restart", m.handleVoiceAction)
	mux.HandleFunc("/api/voice/models/download", m.handleVoiceModelDownload)
	mux.HandleFunc("/api/voice/piper/download", m.handlePiperDownload)
	mux.HandleFunc("/v1/audio/transcriptions", m.withVoiceProvision(m.handleVoiceProxy("/asr")))
	mux.HandleFunc("/v1/audio/speech", m.withVoiceProvision(m.handleVoiceSpeech))
	mux.HandleFunc("/v1/audio/speech/stream", m.withVoiceProvision(m.handleVoiceSpeechStream))
	mux.HandleFunc("/v1/voice/chat/stream", m.withVoiceProvision(m.handleVoiceChatStreamSession))
	mux.HandleFunc("/v1/voice/chat", m.withVoiceProvision(m.handleVoiceChatSessionAdaptive))
	mux.HandleFunc("/v1/", m.handleProxyWithThinking)
	mux.HandleFunc("/", m.handleSimpleUI)

	cfg, _ := loadConfig(configPath)
	cfg = defaults(cfg)
	addr := ":" + get(cfg, "ADMIN_PORT", "11435")
	log.Printf("QnapAssistant management API listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: cors(mux), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("management server stopped: %v", err)
			stop()
		}
	}()

	go func() {
		m.autoProvision()
		cfg, _ := loadConfig(configPath)
		cfg = defaults(cfg)
		if bootstrapSnapshot().Phase != "ready" {
			return
		}
		warmCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		// Remote OpenRouter has no local LLM weights to warm. Local llama.cpp is
		// warmed only when its independent auto-unload switch is disabled.
		if normalizedLLMProvider(cfg) == "local" && !llmAutoUnload(cfg) {
			if err := m.ensureReady(warmCtx); err != nil {
				log.Printf("resident local LLM warmup failed: %v", err)
			} else {
				log.Printf("resident local LLM ready")
			}
		}
		// The voice worker itself is cheap. Start it at boot only when ASR or TTS
		// should be resident; its preload() honors each switch independently.
		if !boolConfig(cfg, "ASR_AUTO_UNLOAD", false) || !boolConfig(cfg, "TTS_AUTO_UNLOAD", false) {
			if err := m.ensureVoiceReady(warmCtx); err != nil {
				log.Printf("resident voice warmup pending/failed: %v", err)
			} else {
				log.Printf("configured resident voice models ready")
			}
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down; unloading voice worker and local LLM")
	_ = m.stopVoiceWorker()
	_ = m.stopBackend()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Sample-Rate, X-Qnap-Voice-Profile, X-Qnap-Voice-Context")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
