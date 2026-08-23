package main

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux"
)

var lastASRUseUnix atomic.Int64
var lastTTSUseUnix atomic.Int64

func markASRUsed() { lastASRUseUnix.Store(time.Now().Unix()) }
func markTTSUsed() { lastTTSUseUnix.Store(time.Now().Unix()) }

func workerPersistentConfig() map[string]string {
	path := strings.TrimSpace(os.Getenv("QNAP_ASSISTANT_CONFIG"))
	if path == "" {
		path = "/share/Public/QnapAssistant/config.env"
	}
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func workerSetting(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if v := strings.TrimSpace(workerPersistentConfig()[key]); v != "" {
		return v
	}
	return fallback
}

func workerBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(workerSetting(key, "")))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return fallback
	}
}

func workerInt(key string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(workerSetting(key, "")))
	if err != nil {
		return fallback
	}
	return n
}

func asrAutoUnloadEnabled() bool { return workerBool("ASR_AUTO_UNLOAD", false) }
func ttsAutoUnloadEnabled() bool { return workerBool("TTS_AUTO_UNLOAD", false) }
func asrIdleSeconds() int         { return workerInt("ASR_IDLE_TIMEOUT_SECONDS", 300) }
func ttsIdleSeconds() int         { return workerInt("TTS_IDLE_TIMEOUT_SECONDS", 300) }

func idleExpired(last int64, seconds int) bool {
	if seconds <= 0 || last <= 0 {
		return false
	}
	return time.Since(time.Unix(last, 0)) >= time.Duration(seconds)*time.Second
}

func (e *engine) unloadASR() bool {
	// Run lock first: a recognizer can never be deleted between ensureASR and
	// Decode. recognize() follows the same run->load order.
	e.asrRunMu.Lock()
	defer e.asrRunMu.Unlock()
	e.asrLoadMu.Lock()
	defer e.asrLoadMu.Unlock()
	if e.asr == nil {
		return false
	}
	sherpa.DeleteOfflineRecognizer(e.asr)
	e.asr = nil
	return true
}

func (e *engine) unloadTTS() bool {
	unloaded := false
	// Piper holds this mutex for a complete synthesis, including restart/retry.
	e.piperRunMu.Lock()
	if e.piperResident != nil {
		e.stopPiperResidentLocked()
		unloaded = true
	}
	e.piperRunMu.Unlock()

	// Supertonic follows run->load ordering just like ASR.
	e.ttsRunMu.Lock()
	e.ttsLoadMu.Lock()
	if e.tts != nil {
		sherpa.DeleteOfflineTts(e.tts)
		e.tts = nil
		unloaded = true
	}
	e.ttsLoadMu.Unlock()
	e.ttsRunMu.Unlock()
	return unloaded
}

func (e *engine) idleUnloadLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if asrAutoUnloadEnabled() && idleExpired(lastASRUseUnix.Load(), asrIdleSeconds()) {
			if e.unloadASR() {
				log.Printf("ASR idle timeout reached; SenseVoice unloaded")
			}
		}
		if ttsAutoUnloadEnabled() && idleExpired(lastTTSUseUnix.Load(), ttsIdleSeconds()) {
			if e.unloadTTS() {
				log.Printf("TTS idle timeout reached; Piper/Supertonic unloaded")
			}
		}
	}
}
