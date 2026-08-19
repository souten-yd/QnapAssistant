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
	order := []string{"MODEL_PATH", "MODEL_DIR", "MODEL_URL", "MODEL_SHA256", "MIN_MODEL_BYTES", "ADMIN_PORT", "BACKEND_PORT", "THREADS", "THREADS_BATCH", "CONTEXT", "BATCH", "UBATCH", "PARALLEL", "THINKING_MODE", "IDLE_TIMEOUT_SECONDS", "EXTRA_ARGS"}
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
		"THINKING_MODE": "off", "IDLE_TIMEOUT_SECONDS": "300", "EXTRA_ARGS": "",
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
