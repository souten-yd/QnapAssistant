package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (m *manager) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "service": "QnapAssistant"})
}

func (m *manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	m.mu.Lock()
	running := m.backendRunningLocked()
	active := m.activeRequests
	dl := m.download
	started := m.startedAt
	m.mu.Unlock()
	var uptime int64
	if running {
		uptime = int64(time.Since(started).Seconds())
	}
	memTotal, memAvail, load1 := systemStats()
	writeJSON(w, map[string]any{
		"manager": true, "llm_loaded": running, "active_requests": active,
		"uptime_seconds": uptime, "idle_timeout_seconds": intVal(cfg, "IDLE_TIMEOUT_SECONDS", 300),
		"model_path": cfg["MODEL_PATH"], "admin_port": cfg["ADMIN_PORT"], "backend_port": cfg["BACKEND_PORT"],
		"memory_total_bytes": memTotal, "memory_available_bytes": memAvail, "load1": load1, "download": dl,
	})
}

func (m *manager) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, _ := loadConfig(m.configPath)
		writeJSON(w, defaults(c))
	case http.MethodPut:
		var incoming config
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&incoming) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		c, _ := loadConfig(m.configPath)
		c = defaults(c)
		allowed := map[string]bool{"MODEL_PATH": true, "MODEL_DIR": true, "MODEL_URL": true, "MODEL_SHA256": true, "MIN_MODEL_BYTES": true, "ADMIN_PORT": true, "BACKEND_PORT": true, "THREADS": true, "THREADS_BATCH": true, "CONTEXT": true, "BATCH": true, "UBATCH": true, "PARALLEL": true, "IDLE_TIMEOUT_SECONDS": true, "EXTRA_ARGS": true}
		for k, v := range incoming {
			if allowed[k] {
				c[k] = strings.TrimSpace(v)
			}
		}
		if err := saveConfig(m.configPath, c); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = m.stopBackend()
		writeJSON(w, map[string]any{"ok": true, "note": "LLM unloaded; new settings apply on next request. ADMIN_PORT changes require QPKG restart."})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *manager) handleProxy(w http.ResponseWriter, r *http.Request) {
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, "LLM startup failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	target, _ := url.Parse("http://127.0.0.1:" + cfg["BACKEND_PORT"])
	m.mu.Lock()
	m.activeRequests++
	m.lastUsed = time.Now()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.activeRequests--
		m.lastUsed = time.Now()
		m.mu.Unlock()
	}()
	p := httputil.NewSingleHostReverseProxy(target)
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, err.Error(), http.StatusBadGateway) }
	p.ServeHTTP(w, r)
}

func (m *manager) handleLogs(w http.ResponseWriter, r *http.Request) {
	path := "/share/Public/QnapAssistant/llama-server.log"
	if r.URL.Query().Get("target") == "admin" {
		path = "/share/Public/QnapAssistant/admin.log"
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 300
	}
	lines, err := tailLines(path, n)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, strings.Join(lines, "\n"))
}

func (m *manager) handleModels(w http.ResponseWriter, r *http.Request) {
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	dir := c["MODEL_DIR"]
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type model struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Selected bool   `json:"selected"`
	}
	out := []model{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		out = append(out, model{Path: p, Name: e.Name(), Size: info.Size(), Selected: p == c["MODEL_PATH"]})
	}
	writeJSON(w, out)
}

func (m *manager) handleModelSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	clean, dir := filepath.Clean(req.Path), filepath.Clean(c["MODEL_DIR"])
	if filepath.Dir(clean) != dir || !strings.HasSuffix(strings.ToLower(clean), ".gguf") {
		http.Error(w, "model must be a GGUF directly under MODEL_DIR", 400)
		return
	}
	if _, err := os.Stat(clean); err != nil {
		http.Error(w, "model not found", 404)
		return
	}
	c["MODEL_PATH"], c["MODEL_SHA256"], c["MODEL_URL"] = clean, "", ""
	if err := saveConfig(m.configPath, c); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = m.stopBackend()
	writeJSON(w, map[string]any{"ok": true, "model_path": clean})
}

func (m *manager) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL      string `json:"URL"`
		Filename string `json:"Filename"`
		SHA256   string `json:"SHA256"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.URL == "" || req.Filename == "" {
		http.Error(w, "url and filename required", 400)
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "download URL must use http or https", 400)
		return
	}
	name := filepath.Base(req.Filename)
	if name != req.Filename || !strings.HasSuffix(strings.ToLower(name), ".gguf") {
		http.Error(w, "filename must end in .gguf", 400)
		return
	}
	c, _ := loadConfig(m.configPath)
	c = defaults(c)
	dest := filepath.Join(c["MODEL_DIR"], name)
	m.mu.Lock()
	if m.download.Active {
		m.mu.Unlock()
		http.Error(w, "a download is already active", 409)
		return
	}
	m.download = downloadState{Active: true, Name: name}
	m.mu.Unlock()
	go m.downloadURL(req.URL, dest, req.SHA256)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "path": dest})
}

func (m *manager) downloadURL(src, dest, wantHash string) {
	defer func() { m.mu.Lock(); m.download.Active = false; m.mu.Unlock() }()
	tmp := dest + ".part"
	_ = os.MkdirAll(filepath.Dir(dest), 0755)
	resp, err := http.Get(src)
	if err != nil {
		m.setDownloadErr(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		m.setDownloadErr(fmt.Errorf("HTTP %s", resp.Status))
		return
	}
	f, err := os.Create(tmp)
	if err != nil {
		m.setDownloadErr(err)
		return
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var written int64
	writer := io.MultiWriter(f, h)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			wn, writeErr := writer.Write(buf[:n])
			written += int64(wn)
			m.mu.Lock()
			m.download.Written, m.download.Total = written, resp.ContentLength
			m.mu.Unlock()
			if writeErr != nil {
				readErr = writeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = f.Close()
			m.setDownloadErr(readErr)
			return
		}
	}
	if err := f.Close(); err != nil {
		m.setDownloadErr(err)
		return
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if wantHash != "" && !strings.EqualFold(actual, wantHash) {
		m.setDownloadErr(fmt.Errorf("SHA-256 mismatch: %s", actual))
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		m.setDownloadErr(err)
		return
	}
	m.mu.Lock()
	m.download.Error = ""
	m.mu.Unlock()
}

func (m *manager) setDownloadErr(err error) {
	m.mu.Lock()
	m.download.Error = err.Error()
	m.mu.Unlock()
}

func (m *manager) handleLLMStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (m *manager) handleLLMStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	_ = m.stopBackend()
	writeJSON(w, map[string]any{"ok": true})
}
func (m *manager) handleLLMRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	_ = m.stopBackend()
	if err := m.ensureReady(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (m *manager) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	lines := make([]string, 0, n)
	for s.Scan() {
		if len(lines) == n {
			copy(lines, lines[1:])
			lines[n-1] = s.Text()
		} else {
			lines = append(lines, s.Text())
		}
	}
	return lines, s.Err()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func systemStats() (total, available int64, load1 string) {
	if f, err := os.Open("/proc/meminfo"); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseInt(fields[1], 10, 64)
			if fields[0] == "MemTotal:" {
				total = v * 1024
			}
			if fields[0] == "MemAvailable:" {
				available = v * 1024
			}
		}
		f.Close()
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			load1 = fields[0]
		}
	}
	return
}
