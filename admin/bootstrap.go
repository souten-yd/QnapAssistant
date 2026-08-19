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
	"time"
)

const (
	openJTalkArchiveURL    = "https://github.com/r9y9/open_jtalk/releases/download/v1.11.1/open_jtalk_dic_utf_8-1.11.tar.gz"
	openJTalkArchiveSHA256 = "fe6ba0e43542cef98339abdffd903e062008ea170b04e7e2a35da805902f382a"
	openJTalkArchiveName   = "open_jtalk_dic_utf_8-1.11.tar.gz"
	openJTalkDirName       = "open_jtalk_dic_utf_8-1.11"
)

type bootstrapInfo struct {
	Active     bool   `json:"active"`
	Phase      string `json:"phase,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

var bootstrapState = struct {
	sync.Mutex
	info bootstrapInfo
}{}

var voiceProvisionMu sync.Mutex

func setBootstrap(active bool, phase, errText string) {
	bootstrapState.Lock()
	defer bootstrapState.Unlock()
	if active && !bootstrapState.info.Active {
		bootstrapState.info.StartedAt = time.Now().Format(time.RFC3339)
		bootstrapState.info.FinishedAt = ""
	}
	bootstrapState.info.Active = active
	bootstrapState.info.Phase = phase
	bootstrapState.info.Error = errText
	if !active {
		bootstrapState.info.FinishedAt = time.Now().Format(time.RFC3339)
	}
}

func bootstrapSnapshot() bootstrapInfo {
	bootstrapState.Lock()
	defer bootstrapState.Unlock()
	return bootstrapState.info
}

func modelReadyForConfig(cfg config) bool {
	st, err := os.Stat(cfg["MODEL_PATH"])
	return err == nil && st.Mode().IsRegular() && st.Size() >= int64(intVal(cfg, "MIN_MODEL_BYTES", 1))
}

func piperOpenJTalkRoot(cfg config) string {
	return filepath.Join(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), "openjtalk")
}

func piperOpenJTalkDir(cfg config) string {
	return filepath.Join(piperOpenJTalkRoot(cfg), openJTalkDirName)
}

func openJTalkDictionaryReady(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".dic" || ext == ".bin" {
			return true
		}
	}
	return false
}

func (m *manager) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	exe, _ := findPiperExecutable(piperRuntimeDirFromConfig(cfg))
	qwen := modelReadyForConfig(cfg)
	asr := voiceModelFilesReady(cfg["ASR_MODEL_DIR"], asrVoiceRequired)
	supertonic := voiceModelFilesReady(cfg["TTS_MODEL_DIR"], ttsVoiceRequired)
	piperModel := piperModelReady(cfg)
	openjtalk := openJTalkDictionaryReady(piperOpenJTalkDir(cfg))
	writeJSON(w, map[string]any{
		"bootstrap":             bootstrapSnapshot(),
		"qwen_ready":            qwen,
		"asr_ready":             asr,
		"supertonic_ready":      supertonic,
		"piper_runtime_ready":   exe != "",
		"piper_model_ready":     piperModel,
		"piper_openjtalk_ready": openjtalk,
		"all_ready":             qwen && asr && supertonic && exe != "" && piperModel && openjtalk,
		"tts_default":           "piper_plus",
	})
}

func (m *manager) autoProvision() {
	setBootstrap(true, "starting", "")
	var failures []string
	setBootstrap(true, "qwen", "")
	if err := m.ensureDefaultLLMModel(); err != nil {
		log.Printf("automatic Qwen provisioning failed: %v", err)
		failures = append(failures, "qwen: "+err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	setBootstrap(true, "voice", "")
	if err := m.ensureVoiceAssets(ctx); err != nil {
		log.Printf("automatic voice provisioning failed: %v", err)
		failures = append(failures, "voice: "+err.Error())
	}
	if len(failures) > 0 {
		setBootstrap(false, "error", strings.Join(failures, "; "))
		return
	}
	setBootstrap(false, "ready", "")
	log.Printf("automatic asset provisioning complete")
}

func (m *manager) ensureDefaultLLMModel() error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	if modelReadyForConfig(cfg) {
		return nil
	}
	dl := filepath.Join(m.qpkgDir, "download-model.sh")
	cmd := exec.Command(dl, m.configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("download default Qwen model: %w", err)
	}
	if !modelReadyForConfig(cfg) {
		return errors.New("default Qwen model download completed but model is not ready")
	}
	return nil
}

func (m *manager) withVoiceProvision(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
		defer cancel()
		if err := m.ensureVoiceAssets(ctx); err != nil {
			http.Error(w, "automatic voice provisioning failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	}
}

func (m *manager) ensureVoiceAssets(ctx context.Context) error {
	voiceProvisionMu.Lock()
	defer voiceProvisionMu.Unlock()
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	if err := m.ensureSherpaVoiceModels(ctx, cfg); err != nil {
		return err
	}
	if err := m.ensurePiperAssets(ctx, cfg); err != nil {
		return err
	}
	if err := ensureOpenJTalkDictionary(ctx, cfg); err != nil {
		return err
	}
	return nil
}

func waitContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *manager) ensureSherpaVoiceModels(ctx context.Context, cfg config) error {
	for {
		if voiceModelFilesReady(cfg["ASR_MODEL_DIR"], asrVoiceRequired) && voiceModelFilesReady(cfg["TTS_MODEL_DIR"], ttsVoiceRequired) {
			return nil
		}
		voiceModelDownload.mu.Lock()
		if !voiceModelDownload.info.Active {
			logPath := filepath.Join(cfg["VOICE_DIR"], "voice-model-download.log")
			voiceModelDownload.info = voiceModelDownloadInfo{Active: true, StartedAt: time.Now().Format(time.RFC3339), LogPath: logPath}
			voiceModelDownload.mu.Unlock()
			m.downloadVoiceModels(cfg, logPath)
			info := voiceModelDownloadSnapshot()
			if info.Error != "" {
				return errors.New(info.Error)
			}
			continue
		}
		voiceModelDownload.mu.Unlock()
		if err := waitContext(ctx, time.Second); err != nil {
			return err
		}
	}
}

func (m *manager) ensurePiperAssets(ctx context.Context, cfg config) error {
	for {
		exe, _ := findPiperExecutable(piperRuntimeDirFromConfig(cfg))
		if exe != "" && piperModelReady(cfg) {
			return nil
		}
		piperDownload.Lock()
		if !piperDownload.info.Active {
			logPath := filepath.Join(cfg["VOICE_DIR"], "piper-plus-download.log")
			piperDownload.info = piperDownloadInfo{Active: true, Phase: "starting", StartedAt: time.Now().Format(time.RFC3339), LogPath: logPath}
			piperDownload.Unlock()
			m.downloadPiper(cfg, logPath)
			info := piperDownloadSnapshot()
			if info.Error != "" {
				return errors.New(info.Error)
			}
			continue
		}
		piperDownload.Unlock()
		if err := waitContext(ctx, time.Second); err != nil {
			return err
		}
	}
}

func ensureOpenJTalkDictionary(ctx context.Context, cfg config) error {
	dictDir := piperOpenJTalkDir(cfg)
	if openJTalkDictionaryReady(dictDir) {
		return nil
	}
	root := piperOpenJTalkRoot(cfg)
	dlDir := filepath.Join(cfg["VOICE_DIR"], ".downloads")
	if err := os.MkdirAll(dlDir, 0755); err != nil {
		return err
	}
	archive := filepath.Join(dlDir, openJTalkArchiveName)
	part := archive + ".part"
	_ = os.Remove(part)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openJTalkArchiveURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return fmt.Errorf("download OpenJTalk dictionary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download OpenJTalk dictionary: HTTP %s", resp.Status)
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	got, err := fileSHA256(part)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, openJTalkArchiveSHA256) {
		_ = os.Remove(part)
		return fmt.Errorf("OpenJTalk dictionary SHA-256 mismatch: got %s", got)
	}
	if err := os.Rename(part, archive); err != nil {
		return err
	}
	if err := extractTarGzSafe(archive, root); err != nil {
		return fmt.Errorf("extract OpenJTalk dictionary: %w", err)
	}
	_ = os.Remove(archive)
	if !openJTalkDictionaryReady(dictDir) {
		return fmt.Errorf("OpenJTalk dictionary extracted but not ready at %s", dictDir)
	}
	return nil
}
