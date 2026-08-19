package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type downloadState struct {
	Active  bool   `json:"active"`
	Name    string `json:"name,omitempty"`
	Written int64  `json:"written_bytes,omitempty"`
	Total   int64  `json:"total_bytes,omitempty"`
	Error   string `json:"error,omitempty"`
}

type manager struct {
	mu             sync.Mutex
	startMu        sync.Mutex
	qpkgDir        string
	configPath     string
	cmd            *exec.Cmd
	startedAt      time.Time
	lastUsed       time.Time
	activeRequests int
	download       downloadState
}

func (m *manager) backendRunningLocked() bool { return m.cmd != nil && m.cmd.Process != nil }

func (m *manager) startBackend() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.backendRunningLocked() {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	model := cfg["MODEL_PATH"]
	if st, err := os.Stat(model); err != nil || st.Size() < int64(intVal(cfg, "MIN_MODEL_BYTES", 1)) {
		dl := filepath.Join(m.qpkgDir, "download-model.sh")
		if err := exec.Command(dl, m.configPath).Run(); err != nil {
			return fmt.Errorf("model unavailable and download failed: %w", err)
		}
	}

	server := filepath.Join(m.qpkgDir, "bin", "llama-server")
	args := []string{"--model", model, "--host", "127.0.0.1", "--port", cfg["BACKEND_PORT"], "--threads", cfg["THREADS"], "--threads-batch", cfg["THREADS_BATCH"], "--ctx-size", cfg["CONTEXT"], "--batch-size", cfg["BATCH"], "--ubatch-size", cfg["UBATCH"], "--parallel", cfg["PARALLEL"]}
	extra, err := splitArgs(cfg["EXTRA_ARGS"])
	if err != nil {
		return err
	}
	args = append(args, extra...)
	logPath := "/share/Public/QnapAssistant/llama-server.log"
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	cmd := exec.Command(server, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		lf.Close()
		return err
	}
	m.mu.Lock()
	m.cmd = cmd
	m.startedAt = time.Now()
	m.lastUsed = time.Now()
	m.mu.Unlock()
	go func(c *exec.Cmd, f *os.File) {
		err := c.Wait()
		f.Close()
		m.mu.Lock()
		if m.cmd == c {
			m.cmd = nil
		}
		m.mu.Unlock()
		if err != nil {
			log.Printf("llama-server exited: %v", err)
		}
	}(cmd, lf)
	return nil
}

func (m *manager) stopBackend() error {
	m.mu.Lock()
	cmd := m.cmd
	if cmd == nil || cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}
	pid := cmd.Process.Pid
	m.cmd = nil
	m.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 20; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return nil
}

func (m *manager) ensureReady(ctx context.Context) error {
	if err := m.startBackend(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	healthURL := "http://127.0.0.1:" + cfg["BACKEND_PORT"] + "/health"
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Get(healthURL)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode < 500 {
					return nil
				}
			}
			m.mu.Lock()
			running := m.backendRunningLocked()
			m.mu.Unlock()
			if !running {
				return errors.New("llama-server exited before becoming ready")
			}
		}
	}
}

func keepModelsLoaded(c config) bool {
	v := strings.ToLower(strings.TrimSpace(get(c, "KEEP_MODELS_LOADED", "1")))
	return v != "0" && v != "false" && v != "off" && v != "no"
}

func (m *manager) idleLoop() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		if keepModelsLoaded(cfg) {
			continue
		}
		idle := intVal(cfg, "IDLE_TIMEOUT_SECONDS", 300)
		if idle <= 0 {
			continue
		}
		m.mu.Lock()
		should := m.backendRunningLocked() && m.activeRequests == 0 && time.Since(m.lastUsed) > time.Duration(idle)*time.Second
		m.mu.Unlock()
		if should {
			log.Printf("idle timeout reached; unloading LLM")
			_ = m.stopBackend()
		}
	}
}
