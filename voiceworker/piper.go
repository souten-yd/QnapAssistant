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

func defaultPiperCompatDir() string {
	if v := strings.TrimSpace(os.Getenv("PIPER_COMPAT_DIR")); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// qnap-voice-worker is installed at <QPKG_DIR>/bin/qnap-voice-worker.
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "piper-compat")
}

func piperCompatRuntime() (loader, libDir string, ok bool) {
	dir := defaultPiperCompatDir()
	if dir == "" {
		return "", "", false
	}
	loader = filepath.Join(dir, "ld-linux-x86-64.so.2")
	libDir = filepath.Join(dir, "lib")
	st, err := os.Stat(loader)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return "", "", false
	}
	if st, err = os.Stat(filepath.Join(libDir, "libc.so.6")); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return "", "", false
	}
	if st, err = os.Stat(filepath.Join(libDir, "libstdc++.so.6")); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return "", "", false
	}
	return loader, libDir, true
}

func (e *engine) piperCompatReady() bool {
	_, _, ok := piperCompatRuntime()
	return ok
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

	runtimeRoot := filepath.Dir(filepath.Dir(exe))
	runtimeLib := filepath.Join(runtimeRoot, "lib")
	loader, compatLib, useCompat := piperCompatRuntime()

	var cmd *exec.Cmd
	if useCompat {
		// The QTS base system on TS-253Be ships a much older glibc/libstdc++ than
		// the official Piper Plus v1.13.0 Linux binary requires. Execute Piper
		// through an isolated loader bundled in the QPKG instead of changing QTS.
		launchArgs := []string{"--library-path", compatLib + ":" + runtimeLib, exe}
		launchArgs = append(launchArgs, args...)
		cmd = exec.Command(loader, launchArgs...)
	} else {
		cmd = exec.Command(exe, args...)
	}
	cmd.Dir = runtimeRoot

	// Keep any first-run OpenJTalk dictionary download persistent and writable
	// under the voice data directory. The manager/worker runs as the QTS service
	// user, so this avoids normal-SSH-user permission issues.
	piperData := filepath.Join(e.cfg.VoiceDir, "piper-data")
	_ = os.MkdirAll(piperData, 0755)
	env := append(os.Environ(),
		"XDG_DATA_HOME="+piperData,
		"PIPER_PLUS_AUTO_DOWNLOAD_DICT=1",
	)
	if !useCompat {
		ld := runtimeLib
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			ld += ":" + existing
		}
		env = append(env, "LD_LIBRARY_PATH="+ld)
	}
	cmd.Env = env

	started := time.Now()
	output, runErr := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if runErr != nil {
		msg := strings.TrimSpace(string(output))
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		mode := "system-loader"
		if useCompat {
			mode = "qts-compat-loader"
		}
		return ttsResult{}, fmt.Errorf("Piper Plus failed (%s): %w: %s", mode, runErr, msg)
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
