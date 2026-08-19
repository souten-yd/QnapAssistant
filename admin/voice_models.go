package main

import (
	"archive/tar"
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	asrVoiceArchiveName = "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17"
	ttsVoiceArchiveName = "sherpa-onnx-supertonic-3-tts-int8-2026-05-11"
	asrVoiceArchiveURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" + asrVoiceArchiveName + ".tar.bz2"
	ttsVoiceArchiveURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/" + ttsVoiceArchiveName + ".tar.bz2"
)

var asrVoiceRequired = []string{"model.int8.onnx", "tokens.txt"}
var ttsVoiceRequired = []string{
	"duration_predictor.int8.onnx",
	"text_encoder.int8.onnx",
	"vector_estimator.int8.onnx",
	"vocoder.int8.onnx",
	"tts.json",
	"unicode_indexer.bin",
	"voice.bin",
}

type voiceModelDownloadInfo struct {
	Active     bool   `json:"active"`
	Current    string `json:"current,omitempty"`
	Written    int64  `json:"written_bytes,omitempty"`
	Total      int64  `json:"total_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
}

type voiceModelDownloadState struct {
	mu   sync.Mutex
	info voiceModelDownloadInfo
}

var voiceModelDownload voiceModelDownloadState

func voiceModelDownloadSnapshot() voiceModelDownloadInfo {
	voiceModelDownload.mu.Lock()
	defer voiceModelDownload.mu.Unlock()
	return voiceModelDownload.info
}

func setVoiceModelDownloadProgress(current string, written, total int64) {
	voiceModelDownload.mu.Lock()
	voiceModelDownload.info.Current = current
	voiceModelDownload.info.Written = written
	voiceModelDownload.info.Total = total
	voiceModelDownload.mu.Unlock()
}

func (m *manager) handleVoiceModelDownload(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		writeJSON(w, map[string]any{
			"download":        voiceModelDownloadSnapshot(),
			"asr_model_ready": voiceModelFilesReady(cfg["ASR_MODEL_DIR"], asrVoiceRequired),
			"tts_model_ready": voiceModelFilesReady(cfg["TTS_MODEL_DIR"], ttsVoiceRequired),
		})
	case http.MethodPost:
		voiceModelDownload.mu.Lock()
		if voiceModelDownload.info.Active {
			info := voiceModelDownload.info
			voiceModelDownload.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]any{"ok": false, "error": "voice model download already running", "download": info})
			return
		}
		cfg, _ := loadConfig(m.configPath)
		cfg = defaults(cfg)
		logPath := filepath.Join(cfg["VOICE_DIR"], "voice-model-download.log")
		voiceModelDownload.info = voiceModelDownloadInfo{
			Active:    true,
			StartedAt: time.Now().Format(time.RFC3339),
			LogPath:   logPath,
		}
		voiceModelDownload.mu.Unlock()
		go m.downloadVoiceModels(cfg, logPath)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"ok": true, "started": true, "status_url": "/api/voice/models/download"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *manager) downloadVoiceModels(cfg config, logPath string) {
	voiceDir := cfg["VOICE_DIR"]
	if err := os.MkdirAll(voiceDir, 0755); err != nil {
		finishVoiceModelDownload(err)
		return
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		finishVoiceModelDownload(err)
		return
	}
	defer lf.Close()
	logger := log.New(lf, "", log.LstdFlags)
	logger.Printf("voice model download started")

	specs := []struct {
		label    string
		name     string
		url      string
		dest     string
		required []string
	}{
		{"SenseVoiceSmall INT8", asrVoiceArchiveName, asrVoiceArchiveURL, cfg["ASR_MODEL_DIR"], asrVoiceRequired},
		{"Supertonic 3 INT8", ttsVoiceArchiveName, ttsVoiceArchiveURL, cfg["TTS_MODEL_DIR"], ttsVoiceRequired},
	}

	for _, spec := range specs {
		if voiceModelFilesReady(spec.dest, spec.required) {
			logger.Printf("%s already ready at %s", spec.label, spec.dest)
			continue
		}
		archiveDir := filepath.Join(voiceDir, ".downloads")
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			finishVoiceModelDownload(err)
			return
		}
		archive := filepath.Join(archiveDir, spec.name+".tar.bz2")
		logger.Printf("downloading %s", spec.url)
		if err := downloadVoiceArchive(spec.label, spec.url, archive); err != nil {
			finishVoiceModelDownload(fmt.Errorf("download %s: %w", spec.label, err))
			return
		}
		logger.Printf("extracting %s to %s", spec.label, spec.dest)
		if err := extractVoiceArchive(archive, spec.name, spec.dest); err != nil {
			finishVoiceModelDownload(fmt.Errorf("extract %s: %w", spec.label, err))
			return
		}
		if !voiceModelFilesReady(spec.dest, spec.required) {
			finishVoiceModelDownload(fmt.Errorf("%s archive extracted but required files are missing", spec.label))
			return
		}
		_ = os.Remove(archive)
		logger.Printf("%s ready", spec.label)
	}

	logger.Printf("all voice models ready")
	finishVoiceModelDownload(nil)
}

func finishVoiceModelDownload(err error) {
	voiceModelDownload.mu.Lock()
	voiceModelDownload.info.Active = false
	voiceModelDownload.info.Current = ""
	voiceModelDownload.info.Written = 0
	voiceModelDownload.info.Total = 0
	voiceModelDownload.info.FinishedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		voiceModelDownload.info.Error = err.Error()
	} else {
		voiceModelDownload.info.Error = ""
	}
	voiceModelDownload.mu.Unlock()
}

func voiceModelFilesReady(dir string, required []string) bool {
	for _, name := range required {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 {
			return false
		}
	}
	return true
}

type voiceDownloadWriter struct {
	w       io.Writer
	label   string
	written int64
	total   int64
}

func (p *voiceDownloadWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	setVoiceModelDownloadProgress(p.label, p.written, p.total)
	return n, err
}

func downloadVoiceArchive(label, url, dst string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	part := dst + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	pw := &voiceDownloadWriter{w: f, label: label, total: resp.ContentLength}
	_, copyErr := io.Copy(pw, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.ContentLength >= 0 && pw.written != resp.ContentLength {
		return fmt.Errorf("short download: got %d bytes, expected %d", pw.written, resp.ContentLength)
	}
	return os.Rename(part, dst)
}

func extractVoiceArchive(archive, rootName, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bzip2.NewReader(f))
	newDir := dest + ".new"
	if err := os.RemoveAll(newDir); err != nil {
		return err
	}
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(newDir)
		}
	}()

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		rel, ok := safeVoiceArchivePath(rootName, h.Name)
		if !ok || rel == "" {
			continue
		}
		target := filepath.Join(newDir, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			// Model archives do not require symlinks/devices; ignore metadata entries.
		}
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(newDir, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func safeVoiceArchivePath(rootName, archiveName string) (string, bool) {
	clean := path.Clean(strings.TrimPrefix(archiveName, "./"))
	root := path.Clean(rootName)
	if clean == root {
		return "", true
	}
	prefix := root + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	relSlash := strings.TrimPrefix(clean, prefix)
	if relSlash == "" || strings.HasPrefix(relSlash, "../") || path.IsAbs(relSlash) {
		return "", false
	}
	rel := filepath.Clean(filepath.FromSlash(relSlash))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}
