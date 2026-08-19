package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type piperResident struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	startedAt time.Time
	requests  int64
}

func (e *engine) piperResidentStatus() (running bool, uptimeSeconds, requests int64) {
	e.piperRunMu.Lock()
	defer e.piperRunMu.Unlock()
	if e.piperResident == nil || e.piperResident.cmd == nil || e.piperResident.cmd.Process == nil {
		return false, 0, 0
	}
	return true, int64(time.Since(e.piperResident.startedAt).Seconds()), e.piperResident.requests
}

func (e *engine) useResidentPiper(o ttsOptions) bool {
	lang := strings.TrimSpace(o.Lang)
	if lang != "" && lang != e.cfg.TTSLanguage {
		return false
	}
	speed := o.Speed
	if speed <= 0 {
		speed = e.cfg.TTSSpeed
	}
	return math.Abs(float64(speed-e.cfg.TTSSpeed)) < 0.0001
}

func (e *engine) startPiperResidentLocked() error {
	if e.piperResident != nil && e.piperResident.cmd != nil && e.piperResident.cmd.Process != nil {
		return nil
	}
	if !e.piperModelReady() {
		return fmt.Errorf("Piper Plus Tsukuyomi model is not installed under %s", e.cfg.PiperModelDir)
	}
	if !e.piperOpenJTalkReady() {
		return fmt.Errorf("Piper Plus OpenJTalk dictionary is not ready under %s", e.piperOpenJTalkDir())
	}
	exe, err := findWorkerPiperExecutable(e.cfg.PiperRuntimeDir)
	if err != nil || exe == "" {
		if err == nil {
			err = fmt.Errorf("bin/piper not found")
		}
		return fmt.Errorf("Piper Plus runtime unavailable: %w", err)
	}

	model := filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperModelFile)
	conf := filepath.Join(e.cfg.PiperModelDir, e.cfg.PiperConfigFile)
	lengthScale := e.cfg.PiperLengthScale
	if lengthScale <= 0 {
		lengthScale = 1
	}
	if e.cfg.TTSSpeed > 0 {
		lengthScale /= e.cfg.TTSSpeed
	}
	noiseScale := e.cfg.PiperNoiseScale
	if noiseScale <= 0 {
		noiseScale = 0.5
	}
	args := []string{
		"--model", model,
		"--config", conf,
		"--language", e.cfg.TTSLanguage,
		"--length-scale", strconv.FormatFloat(float64(lengthScale), 'f', 4, 32),
		"--noise-scale", strconv.FormatFloat(float64(noiseScale), 'f', 4, 32),
		"--json-input",
	}

	runtimeRoot := filepath.Dir(filepath.Dir(exe))
	runtimeLib := filepath.Join(runtimeRoot, "lib")
	loader, compatLib, useCompat := piperCompatRuntime()
	var cmd *exec.Cmd
	if useCompat {
		launchArgs := []string{"--library-path", compatLib + ":" + runtimeLib, exe}
		launchArgs = append(launchArgs, args...)
		cmd = exec.Command(loader, launchArgs...)
	} else {
		cmd = exec.Command(exe, args...)
	}
	cmd.Dir = runtimeRoot
	cmd.Stderr = os.Stderr
	piperData := filepath.Join(e.cfg.VoiceDir, "piper-data")
	_ = os.MkdirAll(piperData, 0755)
	env := append(os.Environ(),
		"XDG_DATA_HOME="+piperData,
		"OPENJTALK_DICTIONARY_PATH="+e.piperOpenJTalkDir(),
		"PIPER_OFFLINE_MODE=1",
		"PIPER_AUTO_DOWNLOAD_DICT=0",
	)
	if !useCompat {
		ld := runtimeLib
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			ld += ":" + existing
		}
		env = append(env, "LD_LIBRARY_PATH="+ld)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	e.piperResident = &piperResident{
		cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), startedAt: time.Now(),
	}
	return nil
}

func (e *engine) stopPiperResidentLocked() {
	p := e.piperResident
	e.piperResident = nil
	if p == nil || p.cmd == nil {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-done
	}
}

func (e *engine) requestPiperResidentLocked(text string) (ttsResult, error) {
	p := e.piperResident
	if p == nil || p.stdin == nil || p.stdout == nil {
		return ttsResult{}, fmt.Errorf("resident Piper process is not running")
	}
	tmp, err := os.CreateTemp("", "qnapassistant-piper-resident-*.wav")
	if err != nil {
		return ttsResult{}, err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName)
	defer os.Remove(tmpName)

	payload, _ := json.Marshal(map[string]any{"text": text, "output_file": tmpName})
	started := time.Now()
	if _, err := p.stdin.Write(append(payload, '\n')); err != nil {
		return ttsResult{}, fmt.Errorf("write resident Piper request: %w", err)
	}
	line, err := p.stdout.ReadString('\n')
	if err != nil {
		return ttsResult{}, fmt.Errorf("read resident Piper response: %w", err)
	}
	reported := strings.TrimSpace(line)
	if reported != "" && filepath.Clean(reported) != filepath.Clean(tmpName) {
		return ttsResult{}, fmt.Errorf("resident Piper returned unexpected output path %q", reported)
	}
	wav, err := os.ReadFile(tmpName)
	if err != nil {
		return ttsResult{}, fmt.Errorf("read resident Piper output: %w", err)
	}
	if len(wav) <= 44 {
		return ttsResult{}, fmt.Errorf("resident Piper produced an empty WAV (%d bytes)", len(wav))
	}
	audio, err := decodeWAV(wav)
	if err != nil {
		return ttsResult{}, fmt.Errorf("decode resident Piper output: %w", err)
	}
	elapsed := time.Since(started)
	sec := float64(len(audio.Samples)) / float64(audio.SampleRate)
	p.requests++
	out := ttsResult{WAV: wav, SampleRate: audio.SampleRate, AudioSec: sec, ProcessMS: elapsed.Milliseconds(), Backend: "piper_plus"}
	if sec > 0 {
		out.RTF = elapsed.Seconds() / sec
	}
	return out, nil
}

func (e *engine) synthesizePiperResident(o ttsOptions) (ttsResult, error) {
	e.piperRunMu.Lock()
	defer e.piperRunMu.Unlock()
	if err := e.startPiperResidentLocked(); err != nil {
		return ttsResult{}, err
	}
	out, err := e.requestPiperResidentLocked(o.Text)
	if err == nil {
		return out, nil
	}
	// A broken pipe or unexpected child exit is recoverable. Restart once and
	// retry the request; subsequent requests remain resident again.
	e.stopPiperResidentLocked()
	if startErr := e.startPiperResidentLocked(); startErr != nil {
		return ttsResult{}, fmt.Errorf("resident Piper failed: %v; restart failed: %w", err, startErr)
	}
	out, retryErr := e.requestPiperResidentLocked(o.Text)
	if retryErr != nil {
		return ttsResult{}, fmt.Errorf("resident Piper failed: %v; retry failed: %w", err, retryErr)
	}
	return out, nil
}
