package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux"
)

type engineConfig struct {
	VoiceDir           string
	ASRModelDir        string
	TTSModelDir        string
	ASRLanguage        string
	TTSLanguage        string
	ASRThreads         int
	TTSThreads         int
	TTSSteps           int
	TTSSpeed           float32
	TTSSid             int
	TTSBackend         string
	TTSFallbackBackend string
	PiperRuntimeDir    string
	PiperModelDir      string
	PiperModelFile     string
	PiperConfigFile    string
	PiperNoiseScale    float32
	PiperLengthScale   float32
}

type engine struct {
	cfg engineConfig

	asrLoadMu sync.Mutex
	asrRunMu  sync.Mutex
	asr       *sherpa.OfflineRecognizer

	ttsLoadMu sync.Mutex
	ttsRunMu  sync.Mutex
	tts       *sherpa.OfflineTts

	piperRunMu   sync.Mutex
	piperResident *piperResident
}

type asrResult struct {
	Text      string  `json:"text"`
	Language  string  `json:"language,omitempty"`
	Emotion   string  `json:"emotion,omitempty"`
	Event     string  `json:"event,omitempty"`
	AudioSec  float64 `json:"audio_seconds"`
	ProcessMS int64   `json:"processing_ms"`
	RTF       float64 `json:"rtf"`
}

type ttsOptions struct {
	Text    string  `json:"text"`
	Lang    string  `json:"lang,omitempty"`
	Speed   float32 `json:"speed,omitempty"`
	Steps   int     `json:"steps,omitempty"`
	Sid     int     `json:"sid,omitempty"`
	Backend string  `json:"backend,omitempty"`
}

type ttsResult struct {
	WAV        []byte
	SampleRate int
	AudioSec   float64
	ProcessMS  int64
	RTF        float64
	Backend    string
}

func (e *engine) asrFilesReady() bool {
	return fileOK(filepath.Join(e.cfg.ASRModelDir, "model.int8.onnx"), 10<<20) && fileOK(filepath.Join(e.cfg.ASRModelDir, "tokens.txt"), 1024)
}

func (e *engine) supertonicFilesReady() bool {
	names := []string{"duration_predictor.int8.onnx", "text_encoder.int8.onnx", "vector_estimator.int8.onnx", "vocoder.int8.onnx", "tts.json", "unicode_indexer.bin", "voice.bin"}
	for _, name := range names {
		if !fileOK(filepath.Join(e.cfg.TTSModelDir, name), 1) {
			return false
		}
	}
	return true
}

func fileOK(path string, min int64) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() >= min
}

func (e *engine) ensureASR() error {
	e.asrLoadMu.Lock()
	defer e.asrLoadMu.Unlock()
	if e.asr != nil {
		return nil
	}
	if !e.asrFilesReady() {
		return fmt.Errorf("SenseVoice model is not installed under %s", e.cfg.ASRModelDir)
	}
	var c sherpa.OfflineRecognizerConfig
	c.FeatConfig.SampleRate = 16000
	c.FeatConfig.FeatureDim = 80
	c.ModelConfig.SenseVoice.Model = filepath.Join(e.cfg.ASRModelDir, "model.int8.onnx")
	c.ModelConfig.SenseVoice.Language = e.cfg.ASRLanguage
	c.ModelConfig.SenseVoice.UseInverseTextNormalization = 1
	c.ModelConfig.Tokens = filepath.Join(e.cfg.ASRModelDir, "tokens.txt")
	c.ModelConfig.NumThreads = e.cfg.ASRThreads
	c.ModelConfig.Provider = "cpu"
	c.ModelConfig.ModelType = "sense_voice"
	c.DecodingMethod = "greedy_search"
	r := sherpa.NewOfflineRecognizer(&c)
	if r == nil {
		return errors.New("failed to create SenseVoice recognizer")
	}
	e.asr = r
	return nil
}

func (e *engine) recognize(a pcmAudio) (asrResult, error) {
	if err := e.ensureASR(); err != nil {
		return asrResult{}, err
	}
	if len(a.Samples) == 0 || a.SampleRate <= 0 {
		return asrResult{}, errors.New("empty audio")
	}
	e.asrRunMu.Lock()
	defer e.asrRunMu.Unlock()
	s := sherpa.NewOfflineStream(e.asr)
	if s == nil {
		return asrResult{}, errors.New("failed to create ASR stream")
	}
	defer sherpa.DeleteOfflineStream(s)
	started := time.Now()
	s.AcceptWaveform(a.SampleRate, a.Samples)
	e.asr.Decode(s)
	r := s.GetResult()
	elapsed := time.Since(started)
	audioSec := float64(len(a.Samples)) / float64(a.SampleRate)
	out := asrResult{AudioSec: audioSec, ProcessMS: elapsed.Milliseconds()}
	if audioSec > 0 {
		out.RTF = elapsed.Seconds() / audioSec
	}
	if r != nil {
		out.Text, out.Language, out.Emotion, out.Event = r.Text, r.Lang, r.Emotion, r.Event
	}
	return out, nil
}

func (e *engine) ensureSupertonic() error {
	e.ttsLoadMu.Lock()
	defer e.ttsLoadMu.Unlock()
	if e.tts != nil {
		return nil
	}
	if !e.supertonicFilesReady() {
		return fmt.Errorf("Supertonic 3 model is not installed under %s", e.cfg.TTSModelDir)
	}
	d := e.cfg.TTSModelDir
	var c sherpa.OfflineTtsConfig
	c.Model.Supertonic.DurationPredictor = filepath.Join(d, "duration_predictor.int8.onnx")
	c.Model.Supertonic.TextEncoder = filepath.Join(d, "text_encoder.int8.onnx")
	c.Model.Supertonic.VectorEstimator = filepath.Join(d, "vector_estimator.int8.onnx")
	c.Model.Supertonic.Vocoder = filepath.Join(d, "vocoder.int8.onnx")
	c.Model.Supertonic.TtsJson = filepath.Join(d, "tts.json")
	c.Model.Supertonic.UnicodeIndexer = filepath.Join(d, "unicode_indexer.bin")
	c.Model.Supertonic.VoiceStyle = filepath.Join(d, "voice.bin")
	c.Model.NumThreads = e.cfg.TTSThreads
	c.Model.Provider = "cpu"
	t := sherpa.NewOfflineTts(&c)
	if t == nil {
		return errors.New("failed to create Supertonic 3 TTS")
	}
	e.tts = t
	return nil
}

func normalizeTTSBackend(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "piper", "piper_plus", "piper-plus":
		return "piper_plus"
	case "supertonic", "supertonic3", "supertonic_3":
		return "supertonic"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func (e *engine) synthesize(o ttsOptions) (ttsResult, error) {
	if o.Text == "" {
		return ttsResult{}, errors.New("text is required")
	}
	explicit := strings.TrimSpace(o.Backend) != ""
	backend := normalizeTTSBackend(o.Backend)
	if backend == "" {
		backend = normalizeTTSBackend(e.cfg.TTSBackend)
	}
	if backend == "" {
		backend = "piper_plus"
	}
	out, err := e.synthesizeBackend(backend, o)
	if err == nil || explicit {
		return out, err
	}
	fallback := normalizeTTSBackend(e.cfg.TTSFallbackBackend)
	if fallback == "" || fallback == backend {
		return out, err
	}
	fallbackOut, fallbackErr := e.synthesizeBackend(fallback, o)
	if fallbackErr == nil {
		fallbackOut.Backend = fallback + "-fallback"
		return fallbackOut, nil
	}
	return ttsResult{}, fmt.Errorf("%s failed: %v; fallback %s failed: %w", backend, err, fallback, fallbackErr)
}

func (e *engine) synthesizeBackend(backend string, o ttsOptions) (ttsResult, error) {
	switch backend {
	case "piper_plus":
		return e.synthesizePiper(o)
	case "supertonic":
		return e.synthesizeSupertonic(o)
	default:
		return ttsResult{}, fmt.Errorf("unsupported TTS backend %q", backend)
	}
}

func (e *engine) synthesizeSupertonic(o ttsOptions) (ttsResult, error) {
	if err := e.ensureSupertonic(); err != nil {
		return ttsResult{}, err
	}
	lang := o.Lang
	if lang == "" {
		lang = e.cfg.TTSLanguage
	}
	if o.Speed <= 0 {
		o.Speed = e.cfg.TTSSpeed
	}
	if o.Steps <= 0 {
		o.Steps = e.cfg.TTSSteps
	}
	if o.Sid < 0 {
		o.Sid = e.cfg.TTSSid
	}
	extra, _ := json.Marshal(map[string]any{"lang": lang})
	gc := sherpa.GenerationConfig{Speed: o.Speed, NumSteps: o.Steps, Sid: o.Sid, Extra: json.RawMessage(extra)}
	e.ttsRunMu.Lock()
	defer e.ttsRunMu.Unlock()
	started := time.Now()
	audio := e.tts.GenerateWithConfig(o.Text, &gc, nil)
	elapsed := time.Since(started)
	if audio == nil || audio.SampleRate <= 0 || len(audio.Samples) == 0 {
		return ttsResult{}, errors.New("TTS generation failed")
	}
	sec := float64(len(audio.Samples)) / float64(audio.SampleRate)
	out := ttsResult{WAV: encodeWAV(audio.Samples, audio.SampleRate), SampleRate: audio.SampleRate, AudioSec: sec, ProcessMS: elapsed.Milliseconds(), Backend: "supertonic"}
	if sec > 0 {
		out.RTF = elapsed.Seconds() / sec
	}
	return out, nil
}

func (e *engine) preload() error {
	var failures []string
	if err := e.ensureASR(); err != nil {
		failures = append(failures, "ASR: "+err.Error())
	}
	if err := e.ensureSupertonic(); err != nil {
		failures = append(failures, "Supertonic: "+err.Error())
	}
	// A tiny resident Piper request blocks until model load + built-in warmup
	// have completed, so health only becomes available after the default TTS is hot.
	if _, err := e.synthesizePiper(ttsOptions{Text: "起動確認", Lang: e.cfg.TTSLanguage, Speed: e.cfg.TTSSpeed}); err != nil {
		failures = append(failures, "Piper: "+err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (e *engine) close() {
	e.piperRunMu.Lock()
	e.stopPiperResidentLocked()
	e.piperRunMu.Unlock()
	e.asrLoadMu.Lock()
	if e.asr != nil {
		sherpa.DeleteOfflineRecognizer(e.asr)
		e.asr = nil
	}
	e.asrLoadMu.Unlock()
	e.ttsLoadMu.Lock()
	if e.tts != nil {
		sherpa.DeleteOfflineTts(e.tts)
		e.tts = nil
	}
	e.ttsLoadMu.Unlock()
}
