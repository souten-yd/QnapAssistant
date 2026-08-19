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
	mux.HandleFunc("/api/config", m.handleConfig)
	mux.HandleFunc("/api/thinking", m.handleThinking)
	mux.HandleFunc("/api/logs", m.handleLogs)
	mux.HandleFunc("/api/models", m.handleModels)
	mux.HandleFunc("/api/models/select", m.handleModelSelect)
	mux.HandleFunc("/api/models/download", m.handleModelDownload)
	mux.HandleFunc("/api/llm/start", m.handleLLMStart)
	mux.HandleFunc("/api/llm/stop", m.handleLLMStop)
	mux.HandleFunc("/api/llm/restart", m.handleLLMRestart)
	mux.HandleFunc("/api/voice/status", m.handleVoiceStatus)
	mux.HandleFunc("/api/voice/start", m.handleVoiceAction)
	mux.HandleFunc("/api/voice/stop", m.handleVoiceAction)
	mux.HandleFunc("/api/voice/restart", m.handleVoiceAction)
	mux.HandleFunc("/v1/audio/transcriptions", m.handleVoiceProxy("/asr"))
	mux.HandleFunc("/v1/audio/speech", m.handleVoiceProxy("/tts"))
	mux.HandleFunc("/v1/voice/chat", m.handleVoiceChat)
	mux.HandleFunc("/v1/", m.handleProxyWithThinking)
	mux.HandleFunc("/", m.handleUI)

	cfg, _ := loadConfig(configPath)
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
	<-ctx.Done()
	log.Printf("shutting down; unloading voice worker and LLM")
	_ = m.stopVoiceWorker()
	_ = m.stopBackend()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Sample-Rate")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
