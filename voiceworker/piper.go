package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (e *engine) piperModelReady() bool {
	return fileOK(filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperModelFile), 10<<20) &&
		fileOK(filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperConfigFile), 1024)
}

func (e *engine) piperRuntimeReady() bool {
	exe, _ := findWorkerPiperExecutable(e.cfg.PiperRuntimeDir)
	return exe != ""
}

func findWorkerPiperExecutable(runtimeDir string) (string, error) {
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

func (e *engine) synthesizePiper(o ttsOptions) (ttsResult, error) {
	if !e.piperModelReady() {
		return ttsResult{}, fmt.Errorf("Piper Plus Tsukuyomi model is not installed under %s", e.cfg.PiperModelDir)
	}
	exe, err := findWorkerPiperExecutable(e.cfg.PiperRuntimeDir)
	if err != nil || exe == "" {
		if err == nil {
			err = fmt.Errorf("bin/piper not found")
		}
		return ttsResult{}, fmt.Errorf("Piper Plus runtime unavailable: %w", err)
	}
	lang := strings.TrimSpace(o.Lang)
	if lang == "" {
		lang = e.cfg.TTSLanguage
	}
	speed := o.Speed
	if speed <= 0 {
		speed = e.cfg.TTSSpeed
	}
	lengthScale := e.cfg.PiperLengthScale
	if lengthScale <= 0 {
		lengthScale = 1
	}
	if speed > 0 {
		lengthScale /= speed
	}
	noiseScale := e.cfg.PiperNoiseScale
	if noiseScale <= 0 {
		noiseScale = 0.5
	}

	tmp, err := os.CreateTemp("", "qnapassistant-piper-*.wav")
	if err != nil {
		return ttsResult{}, err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName)
	defer os.Remove(tmpName)

	model := filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperModelFile)
	conf := filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperConfigFile)
	args := []string{
		"--model", model,
		"--config", conf,
		"--text", o.Text,
		"--output_file", tmpName,
		"--language", lang,
		"--length-scale", strconv.FormatFloat(float64(lengthScale), 'f', 4, 32),
		"--noise-scale", strconv.FormatFloat(float64(noiseScale), 'f', 4, 32),
	}

	e.piperRunMu.Lock()
	defer e.piperRunMu.Unlock()

	started := time.Now()
	cmd := exec.Command(exe, args...)
	runtimeRoot := filepath.Dir(filepath.Dir(exe))
	cmd.Dir = runtimeRoot
	libDir := filepath.Join(runtimeRoot, "lib")
	ld := libDir
	if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
		ld += ":" + existing
	}
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+ld)
	output, runErr := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if runErr != nil {
		msg := strings.TrimSpace(string(output))
		if len(msg) > 1500 {
			msg = msg[len(msg)-1500:]
		}
		return ttsResult{}, fmt.Errorf("Piper Plus failed: %w: %s", runErr, msg)
	}

	wav, err := os.ReadFile(tmpName)
	if err != nil {
		return ttsResult{}, fmt.Errorf("read Piper output: %w", err)
	}
	audio, err := decodeWAV(wav)
	if err != nil {
		return ttsResult{}, fmt.Errorf("decode Piper output: %w", err)
	}
	sec := float64(len(audio.Samples)) / float64(audio.SampleRate)
	out := ttsResult{
		WAV: wav, SampleRate: audio.SampleRate, AudioSec: sec,
		ProcessMS: elapsed.Milliseconds(), Backend: "piper_plus",
	}
	if sec > 0 {
		out.RTF = elapsed.Seconds() / sec
	}
	return out, nil
}
