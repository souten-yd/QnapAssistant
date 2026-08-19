package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	piperRuntimeURL    = "https://github.com/ayutaz/piper-plus/releases/download/v1.13.0/piper-linux-x64.tar.gz"
	piperRuntimeSHA256 = "1820316ad84ee864d11a8bd7297796e1f773775fc4b004a1c80bb899ac42118b"
	piperModelURL      = "https://huggingface.co/ayousanz/piper-plus-tsukuyomi-chan/resolve/main/tsukuyomi-chan-6lang-fp16.onnx?download=true"
	piperModelSHA256   = "5289e9b6eaf21080803b7fe1c4dc85b5491d4c216121207a41df18dd5f68e5d7"
	piperConfigURL     = "https://huggingface.co/ayousanz/piper-plus-tsukuyomi-chan/resolve/main/config.json?download=true"
)

func piperRuntimeDirFromConfig(cfg config) string {
	return get(cfg, "PIPER_RUNTIME_DIR", "/share/Public/QnapAssistant/voice/piper-plus-runtime")
}

func piperModelDirFromConfig(cfg config) string {
	return get(cfg, "PIPER_MODEL_DIR", "/share/Public/QnapAssistant/voice/piper-plus-tsukuyomi")
}

func piperModelFileFromConfig(cfg config) string {
	return get(cfg, "PIPER_MODEL_FILE", "tsukuyomi-chan-6lang-fp16.onnx")
}

func piperConfigFileFromConfig(cfg config) string {
	return get(cfg, "PIPER_CONFIG_FILE", "config.json")
}

type piperDownloadInfo struct {
	Active     bool   `json:"active"`
	Phase      string `json:"phase,omitempty"`
	Current    string `json:"current,omitempty"`
	Written    int64  `json:"written_bytes,omitempty"`
	Total      int64  `json:"total_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
}

var piperDownload = struct {
	sync.Mutex
	info piperDownloadInfo
}{}

func piperDownloadSnapshot() piperDownloadInfo {
	piperDownload.Lock()
	defer piperDownload.Unlock()
	return piperDownload.info
}

func setPiperProgress(phase, current string, written, total int64) {
	piperDownload.Lock()
	piperDownload.info.Phase = phase
	piperDownload.info.Current = current
	piperDownload.info.Written = written
	piperDownload.info.Total = total
	piperDownload.Unlock()
}

func (m *manager) handlePiperDownload(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	switch r.Method {
	case http.MethodGet:
		exe, _ := findPiperExecutable(piperRuntimeDirFromConfig(cfg))
		writeJSON(w, map[string]any{
			"download":           piperDownloadSnapshot(),
			"runtime_ready":      exe != "",
			"runtime_executable": exe,
			"model_ready":        piperModelReady(cfg),
			"runtime_version":    "v1.13.0",
			"model_sha256":       piperModelSHA256,
			"runtime_sha256":     piperRuntimeSHA256,
			"tts_candidate":      "piper_plus",
		})
	case http.MethodPost:
		piperDownload.Lock()
		if piperDownload.info.Active {
			info := piperDownload.info
			piperDownload.Unlock()
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]any{"ok": false, "error": "Piper Plus download already running", "download": info})
			return
		}
		logPath := filepath.Join(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), "piper-plus-download.log")
		piperDownload.info = piperDownloadInfo{
			Active:    true,
			Phase:     "starting",
			StartedAt: time.Now().Format(time.RFC3339),
			LogPath:   logPath,
		}
		piperDownload.Unlock()
		go m.downloadPiper(cfg, logPath)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"ok": true, "started": true, "status_url": "/api/voice/piper/download"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *manager) downloadPiper(cfg config, logPath string) {
	if err := os.MkdirAll(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), 0755); err != nil {
		finishPiperDownload(err)
		return
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		finishPiperDownload(err)
		return
	}
	defer lf.Close()
	logger := log.New(lf, "", log.LstdFlags)
	logger.Printf("Piper Plus candidate download started")

	if exe, _ := findPiperExecutable(piperRuntimeDirFromConfig(cfg)); exe == "" {
		dlDir := filepath.Join(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), ".downloads")
		if err := os.MkdirAll(dlDir, 0755); err != nil {
			finishPiperDownload(err)
			return
		}
		archive := filepath.Join(dlDir, "piper-linux-x64-v1.13.0.tar.gz")
		logger.Printf("downloading Piper Plus v1.13.0 runtime")
		if err := downloadPiperFile("runtime", "Piper Plus v1.13.0 runtime", piperRuntimeURL, archive, piperRuntimeSHA256); err != nil {
			finishPiperDownload(err)
			return
		}
		setPiperProgress("extracting", "Piper Plus v1.13.0 runtime", 0, 0)
		logger.Printf("extracting Piper Plus runtime")
		if err := extractTarGzSafe(archive, piperRuntimeDirFromConfig(cfg)); err != nil {
			finishPiperDownload(fmt.Errorf("extract Piper runtime: %w", err))
			return
		}
		_ = os.Remove(archive)
		exe, _ := findPiperExecutable(piperRuntimeDirFromConfig(cfg))
		if exe == "" {
			finishPiperDownload(errors.New("Piper runtime extracted but bin/piper was not found"))
			return
		}
		_ = os.Chmod(exe, 0755)
		logger.Printf("Piper runtime ready: %s", exe)
	} else {
		logger.Printf("Piper runtime already ready: %s", exe)
	}

	if !piperModelReady(cfg) {
		if err := os.MkdirAll(piperModelDirFromConfig(cfg), 0755); err != nil {
			finishPiperDownload(err)
			return
		}
		modelPath := filepath.Join(piperModelDirFromConfig(cfg), piperModelFileFromConfig(cfg))
		configPath := filepath.Join(piperModelDirFromConfig(cfg), piperConfigFileFromConfig(cfg))
		logger.Printf("downloading Tsukuyomi-chan MB-iSTFT-VITS2 model")
		if err := downloadPiperFile("downloading", "Tsukuyomi-chan model", piperModelURL, modelPath, piperModelSHA256); err != nil {
			finishPiperDownload(err)
			return
		}
		if err := downloadPiperFile("downloading", "Tsukuyomi-chan config", piperConfigURL, configPath, ""); err != nil {
			finishPiperDownload(err)
			return
		}
		if !piperModelReady(cfg) {
			finishPiperDownload(errors.New("Piper model download completed but required files are missing"))
			return
		}
		logger.Printf("Tsukuyomi-chan model ready")
	} else {
		logger.Printf("Tsukuyomi-chan model already ready")
	}

	logger.Printf("Piper Plus candidate is ready for physical benchmark")
	finishPiperDownload(nil)
}

func finishPiperDownload(err error) {
	piperDownload.Lock()
	piperDownload.info.Active = false
	piperDownload.info.Phase = "complete"
	piperDownload.info.Current = ""
	piperDownload.info.Written = 0
	piperDownload.info.Total = 0
	piperDownload.info.FinishedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		piperDownload.info.Phase = "error"
		piperDownload.info.Error = err.Error()
	} else {
		piperDownload.info.Error = ""
	}
	piperDownload.Unlock()
}

type piperProgressWriter struct {
	w       io.Writer
	phase   string
	label   string
	written int64
	total   int64
}

func (p *piperProgressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	setPiperProgress(p.phase, p.label, p.written, p.total)
	return n, err
}

func downloadPiperFile(phase, label, url, dst, expectedSHA string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %s", label, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	part := dst + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	pw := &piperProgressWriter{w: f, phase: phase, label: label, total: resp.ContentLength}
	_, copyErr := io.Copy(pw, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.ContentLength >= 0 && pw.written != resp.ContentLength {
		return fmt.Errorf("%s: short download: got %d bytes, expected %d", label, pw.written, resp.ContentLength)
	}
	if expectedSHA != "" {
		got, err := fileSHA256(part)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, expectedSHA) {
			_ = os.Remove(part)
			return fmt.Errorf("%s: SHA-256 mismatch: got %s", label, got)
		}
	}
	return os.Rename(part, dst)
}

func fileSHA256(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func piperModelReady(cfg config) bool {
	model, err1 := os.Stat(filepath.Join(piperModelDirFromConfig(cfg), piperModelFileFromConfig(cfg)))
	conf, err2 := os.Stat(filepath.Join(piperModelDirFromConfig(cfg), piperConfigFileFromConfig(cfg)))
	return err1 == nil && err2 == nil && model.Mode().IsRegular() && conf.Mode().IsRegular() && model.Size() > 10<<20 && conf.Size() > 1024
}

func findPiperExecutable(runtimeDir string) (string, error) {
	candidates := []string{
		filepath.Join(runtimeDir, "bin", "piper"),
		filepath.Join(runtimeDir, "piper", "bin", "piper"),
		filepath.Join(runtimeDir, "bin", "piper-plus"),
		filepath.Join(runtimeDir, "piper-plus", "bin", "piper-plus"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
	}
	var found string
	err := filepath.WalkDir(runtimeDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		if (base == "piper" || base == "piper-plus") && filepath.Base(filepath.Dir(p)) == "bin" {
			found = p
		}
		return nil
	})
	return found, err
}

func extractTarGzSafe(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	stage := dest + ".new"
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0755); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, ok := safeTarTarget(stage, h.Name)
		if !ok {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		mode := os.FileMode(h.Mode) & 0777
		switch h.Typeflag {
		case tar.TypeDir:
			if mode == 0 {
				mode = 0755
			}
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if mode == 0 {
				mode = 0644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
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
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(h.Linkname)))
			if !pathWithin(stage, linkTarget) || filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("unsafe symlink %q -> %q", h.Name, h.Linkname)
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, ok := safeTarTarget(stage, h.Linkname)
			if !ok {
				return fmt.Errorf("unsafe hardlink %q -> %q", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			// Ignore tar metadata entries and device nodes.
		}
	}

	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func safeTarTarget(root, archiveName string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(archiveName, "./")))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	target := filepath.Join(root, clean)
	return target, pathWithin(root, target)
}

func pathWithin(root, target string) bool {
	ra, err1 := filepath.Abs(root)
	ta, err2 := filepath.Abs(target)
	if err1 != nil || err2 != nil {
		return false
	}
	return ta == ra || strings.HasPrefix(ta, ra+string(filepath.Separator))
}
