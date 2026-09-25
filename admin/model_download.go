package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bound connection/header and stalled-body waits, not the duration of a healthy
// multi-GB transfer. Keep normal certificate verification and proxy support.
var modelDownloadTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return t
}()
var modelDownloadClient = &http.Client{Transport: modelDownloadTransport}

func (m *manager) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		m.mu.Lock()
		state := m.download
		m.mu.Unlock()
		if state.Status == "" {
			state.Status = "idle"
		}
		writeJSON(w, state)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL      string
		Filename string
		SHA256   string
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil {
		http.Error(w, "invalid download request", 400)
		return
	}
	req.URL, req.Filename, req.SHA256 = strings.TrimSpace(req.URL), strings.TrimSpace(req.Filename), strings.TrimSpace(req.SHA256)
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "download URL must use http or https with a host", 400)
		return
	}
	// Hugging Face's file browser URL returns HTML, not model bytes.
	if strings.EqualFold(u.Hostname(), "huggingface.co") {
		u.Path = strings.Replace(u.Path, "/blob/", "/resolve/", 1)
	}
	if req.Filename == "" || filepath.Base(req.Filename) != req.Filename || !strings.HasSuffix(strings.ToLower(req.Filename), ".gguf") {
		http.Error(w, "filename must be a .gguf file directly under MODEL_DIR", 400)
		return
	}
	if req.SHA256 != "" {
		digest, err := hex.DecodeString(req.SHA256)
		if err != nil || len(digest) != sha256.Size {
			http.Error(w, "SHA-256 must contain 64 hexadecimal characters", 400)
			return
		}
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	cfg = defaults(cfg)
	dest := filepath.Join(cfg["MODEL_DIR"], req.Filename)
	m.mu.Lock()
	if m.download.Active {
		m.mu.Unlock()
		http.Error(w, "a download is already active", 409)
		return
	}
	m.download = downloadState{Active: true, Name: req.Filename, Path: dest, Status: "connecting", StartedAt: time.Now()}
	m.mu.Unlock()
	go m.downloadURL(u.String(), dest, req.SHA256)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "path": dest})
}

func (m *manager) downloadURL(src, dest, wantHash string) {
	defer func() { m.mu.Lock(); m.download.Active = false; m.download.FinishedAt = time.Now(); m.mu.Unlock() }()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		m.setDownloadErr(fmt.Errorf("create model directory: %w", err))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		m.setDownloadErr(err)
		return
	}
	req.Header.Set("User-Agent", "QnapAssistant-model-download")
	resp, err := modelDownloadClient.Do(req)
	if err != nil {
		m.setDownloadErr(fmt.Errorf("connect to model host: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.setDownloadErr(fmt.Errorf("model host returned HTTP %s; check the direct download URL and access permissions", resp.Status))
		return
	}
	idle := time.AfterFunc(60*time.Second, cancel)
	defer idle.Stop()
	// Reject HTML/login pages and error payloads even when served with HTTP 200.
	magic := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, magic); err != nil {
		m.setDownloadErr(fmt.Errorf("read GGUF header: %w", err))
		return
	}
	if string(magic) != "GGUF" {
		m.setDownloadErr(fmt.Errorf("download is not a GGUF model; use the direct file URL, not a web/login page"))
		return
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		m.setDownloadErr(fmt.Errorf("create model file: %w", err))
		return
	}
	defer os.Remove(tmp)
	defer f.Close()
	h := sha256.New()
	writer := io.MultiWriter(f, h)
	if _, err := writer.Write(magic); err != nil {
		m.setDownloadErr(err)
		return
	}
	written := int64(len(magic))
	started := time.Now()
	update := func() {
		m.mu.Lock()
		m.download.Status = "downloading"
		m.download.Written, m.download.Total = written, resp.ContentLength
		m.download.BytesPerSecond = float64(written) / time.Since(started).Seconds()
		m.mu.Unlock()
	}
	update()
	buf := make([]byte, 1<<20)
	for {
		idle.Reset(60 * time.Second)
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			wn, writeErr := writer.Write(buf[:n])
			written += int64(wn)
			update()
			if writeErr != nil {
				m.setDownloadErr(fmt.Errorf("write model file (check free disk space/permissions): %w", writeErr))
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			m.setDownloadErr(fmt.Errorf("download interrupted (60s without data times out); retry download: %w", readErr))
			return
		}
	}
	idle.Stop()
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		m.setDownloadErr(fmt.Errorf("incomplete download: got %d of %d bytes", written, resp.ContentLength))
		return
	}
	if written < 24 {
		m.setDownloadErr(fmt.Errorf("GGUF file is truncated"))
		return
	}
	m.mu.Lock()
	m.download.Status = "verifying"
	m.mu.Unlock()
	if err := f.Close(); err != nil {
		m.setDownloadErr(err)
		return
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if wantHash != "" && !strings.EqualFold(actual, wantHash) {
		m.setDownloadErr(fmt.Errorf("SHA-256 mismatch: got %s", actual))
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		m.setDownloadErr(fmt.Errorf("install model file: %w", err))
		return
	}
	m.mu.Lock()
	m.download.Status = "complete"
	m.download.Error = ""
	m.mu.Unlock()
}

func (m *manager) setDownloadErr(err error) {
	m.mu.Lock()
	m.download.Status, m.download.Error = "failed", err.Error()
	m.mu.Unlock()
}
