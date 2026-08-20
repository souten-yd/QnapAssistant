package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type config map[string]string

func loadConfig(path string) (config, error) {
	c := config{}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			c[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return c, s.Err()
}

func saveConfig(path string, c config) error {
	order := []string{
		"MODEL_PATH", "MODEL_DIR", "MODEL_URL", "MODEL_SHA256", "MIN_MODEL_BYTES",
		"ADMIN_PORT", "BACKEND_PORT", "THREADS", "THREADS_BATCH", "CONTEXT", "BATCH", "UBATCH", "PARALLEL", "THINKING_MODE", "KEEP_MODELS_LOADED", "IDLE_TIMEOUT_SECONDS", "EXTRA_ARGS",
		"VOICE_PORT", "VOICE_DIR", "ASR_MODEL_DIR", "TTS_MODEL_DIR", "ASR_LANGUAGE", "TTS_LANGUAGE", "ASR_THREADS", "TTS_THREADS", "TTS_STEPS", "TTS_SPEED", "TTS_SID", "VOICE_MAX_TOKENS",
		"VOICE_REPLY_MAX_TOKENS", "VOICE_REPLY_TEMPERATURE", "VOICE_SYSTEM_PROMPT", "VOICE_PROFILE_DEFAULT",
		"VOICE_GENERIC_SAMPLE_RATE", "VOICE_GENERIC_PEAK_TARGET", "VOICE_GENERIC_STRIP_EMOJI", "VOICE_GENERIC_STREAM_FORMAT", "VOICE_GENERIC_CHUNK_MIN_CHARS", "VOICE_GENERIC_CHUNK_MAX_CHARS",
		"VOICE_M5_SAMPLE_RATE", "VOICE_M5_PEAK_TARGET", "VOICE_M5_STRIP_EMOJI", "VOICE_M5_STREAM_FORMAT", "VOICE_M5_CHUNK_MIN_CHARS", "VOICE_M5_CHUNK_MAX_CHARS",
	}
	var b strings.Builder
	b.WriteString("# QnapAssistant persistent configuration\n")
	for _, k := range order {
		if v, ok := c[k]; ok {
			fmt.Fprintf(&b, "%s=%s\n", k, strings.ReplaceAll(v, "\n", ""))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func defaults(c config) config {
	if c == nil {
		c = config{}
	}
	defs := config{
		"MODEL_PATH": "/share/Public/Qwen3-0.6B-Q8_0.gguf", "MODEL_DIR": "/share/Public",
		"MODEL_URL": "https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/1eaf4d9657fe65ad10a51eab76a8db5b363bddaa/Qwen3-0.6B-Q8_0.gguf?download=true",
		"MODEL_SHA256": "9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031",
		"MIN_MODEL_BYTES": "100000000", "ADMIN_PORT": "11435", "BACKEND_PORT": "11436",
		"THREADS": "4", "THREADS_BATCH": "4", "CONTEXT": "4096", "BATCH": "256", "UBATCH": "128", "PARALLEL": "1",
		"THINKING_MODE": "off", "KEEP_MODELS_LOADED": "1", "IDLE_TIMEOUT_SECONDS": "0", "EXTRA_ARGS": "",
		"VOICE_PORT": "11437", "VOICE_DIR": "/share/Public/QnapAssistant/voice",
		"ASR_MODEL_DIR": "/share/Public/QnapAssistant/voice/sensevoice", "TTS_MODEL_DIR": "/share/Public/QnapAssistant/voice/supertonic3",
		"ASR_LANGUAGE": "ja", "TTS_LANGUAGE": "ja", "ASR_THREADS": "4", "TTS_THREADS": "2", "TTS_STEPS": "4", "TTS_SPEED": "1.0", "TTS_SID": "0", "VOICE_MAX_TOKENS": "128",
		// 0 means omit max_tokens and inherit the OpenAI-compatible backend's
		// standard completion limit. Set a positive value only when a client or
		// deployment intentionally wants a hard reply cap.
		"VOICE_REPLY_MAX_TOKENS": "0", "VOICE_REPLY_TEMPERATURE": "0.2",
		"VOICE_SYSTEM_PROMPT": "あなたは音声アシスタントです。ユーザーの発話内容を踏まえて自然な日本語で答えてください。入力内容をそのまま繰り返すだけの返答を避け、質問や依頼に直接答えてください。説明が必要な場合は省略せず、内容に応じた必要十分な長さで回答してください。音声で不自然なMarkdown記号や絵文字は避けてください。",
		"VOICE_PROFILE_DEFAULT": "generic",
		"VOICE_GENERIC_SAMPLE_RATE": "0", "VOICE_GENERIC_PEAK_TARGET": "0", "VOICE_GENERIC_STRIP_EMOJI": "0", "VOICE_GENERIC_STREAM_FORMAT": "ndjson", "VOICE_GENERIC_CHUNK_MIN_CHARS": "12", "VOICE_GENERIC_CHUNK_MAX_CHARS": "28",
		"VOICE_M5_SAMPLE_RATE": "16000", "VOICE_M5_PEAK_TARGET": "0.12", "VOICE_M5_STRIP_EMOJI": "1", "VOICE_M5_STREAM_FORMAT": "multipart", "VOICE_M5_CHUNK_MIN_CHARS": "8", "VOICE_M5_CHUNK_MAX_CHARS": "18",
	}
	for k, v := range defs {
		if _, ok := c[k]; !ok {
			c[k] = v
		}
	}
	return c
}

func get(c config, k, d string) string {
	if v := c[k]; v != "" {
		return v
	}
	return d
}
func intVal(c config, k string, d int) int {
	n, err := strconv.Atoi(get(c, k, ""))
	if err != nil {
		return d
	}
	return n
}
