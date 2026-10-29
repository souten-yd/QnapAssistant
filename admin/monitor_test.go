package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lines taken from a TS-253Be llama-server log.
const sampleLlamaLog = `0.00.034.864 I srv    load_model: loading model '/share/Public/Qwen3-4B-Instruct-2507-Q4_K_M.gguf'
0.34.267.358 I srv  llama_server: listening on http://127.0.0.1:11436
0.59.120.702 I slot launch_slot_: id  0 | task 0 | processing task, is_child = 0
2.42.175.871 I slot print_timing: id  0 | task 0 | prompt eval time =   94592.10 ms /    16 tokens ( 5912.01 ms per token,     0.17 tokens per second)
2.42.185.953 I slot print_timing: id  0 | task 0 |        eval time =    8463.00 ms /     2 tokens ( 8463.00 ms per token,     0.12 tokens per second)
2.42.197.835 I slot      release: id  0 | task 0 | stop processing: n_tokens = 17, truncated = 0
0.20.054.322 I slot launch_slot_: id  0 | task 7 | processing task, is_child = 0
17.33.225.942 I slot print_timing: id  0 | task 7 | prompt processing, n_tokens =    256, progress = 0.36, t = 1033.17 s / 0.25 tokens per second`

func TestParseLlamaLogLiveProgressAndLastTimings(t *testing.T) {
	s := parseLlamaLog(strings.Split(sampleLlamaLog, "\n"))
	if !s.Processing {
		t.Fatal("expected processing")
	}
	if s.PromptTokensDone != 256 || s.PromptProgress != 0.36 || s.PromptTokensTotal != 711 || s.PromptTokensPerSec != 0.25 {
		t.Fatalf("progress: %+v", s)
	}
	if s.GenTokensPerSec != 0.12 || s.LastGeneratedTokens != 2 || s.LastPromptTokens != 16 {
		t.Fatalf("timings: %+v", s)
	}
	if s.LoadedModel != "Qwen3-4B-Instruct-2507-Q4_K_M.gguf" {
		t.Fatalf("model: %q", s.LoadedModel)
	}
	// After the task is released, no live progress is reported.
	done := parseLlamaLog(strings.Split(sampleLlamaLog+"\n18.00.000.000 I slot      release: id  0 | task 7 | stop processing: n_tokens = 712, truncated = 0", "\n"))
	if done.Processing || done.PromptProgress != -1 {
		t.Fatalf("released: %+v", done)
	}
	if empty := parseLlamaLog(nil); empty.Processing || empty.PromptProgress != -1 {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestQuantOfAndRedaction(t *testing.T) {
	cases := map[string]string{
		"/share/Public/Qwen3-4B-Instruct-2507-Q4_K_M.gguf": "Q4_K_M", "Qwen3-0.6B-Q8_0.gguf": "Q8_0",
		"Qwen3-4B-Instruct-2507-Q4_0.gguf": "Q4_0", "model-IQ4_XS.gguf": "IQ4_XS", "plain.gguf": "",
	}
	for in, want := range cases {
		if got := quantOf(in); got != want {
			t.Errorf("quantOf(%q)=%q want %q", in, got, want)
		}
	}
	red := redactConfig(config{"OPENROUTER_API_KEY": "sk-or-abcdef123456", "EXTRA_ARGS": "--header Bearer abc.def", "CONTEXT": "4096"})
	if red["OPENROUTER_API_KEY"] != "(redacted)" || strings.Contains(red["EXTRA_ARGS"], "abc.def") || red["CONTEXT"] != "4096" {
		t.Fatalf("redaction: %v", red)
	}
}

func TestJobHistorySummaryAndLimit(t *testing.T) {
	t0 := time.Unix(1000, 0)
	j := &llmJob{created: t0, loadStartedAt: t0.Add(2 * time.Second), readyAt: t0.Add(32 * time.Second),
		generationStarted: t0.Add(33 * time.Second), firstTokenAt: t0.Add(93 * time.Second), finished: t0.Add(113 * time.Second),
		state: "completed", model: "/models/a-Q4_0.gguf", completionTokens: 40, promptTokens: 700}
	e := summarizeJob(j)
	if e.QueueSeconds != 2 || e.LoadSeconds != 30 || e.FirstTokenSeconds != 60 || e.GenerateSeconds != 80 || e.TokensPerSecond != 2 || e.Model != "a-Q4_0.gguf" {
		t.Fatalf("summary: %+v", e)
	}
	timedOut := summarizeJob(&llmJob{created: t0, generationStarted: t0, finished: t0.Add(time.Hour), state: "timed_out", errorCode: "generation_first_token_timeout"})
	if timedOut.FirstTokenSeconds != -1 || timedOut.TokensPerSecond != 0 {
		t.Fatalf("timed out: %+v", timedOut)
	}
	h := &jobHistory{}
	for i := 0; i < jobHistoryLimit+5; i++ {
		h.add(jobHistoryEntry{InputBytes: i})
	}
	list := h.list()
	if len(list) != jobHistoryLimit || list[0].InputBytes != jobHistoryLimit+4 {
		t.Fatalf("history: len=%d first=%d", len(list), list[0].InputBytes)
	}
}

func TestMarkJobRecordsHistoryOnce(t *testing.T) {
	recentJobs = &jobHistory{}
	m := &manager{}
	j := &llmJob{created: time.Now(), state: "queued"}
	m.markJob(j, "loading_model", "", "", nil)
	m.markJob(j, "ready", "", "", nil)
	m.markJob(j, "failed", "model_start_failed", "x", nil)
	m.markJob(j, "completed", "", "", nil) // ignored: already terminal
	list := recentJobs.list()
	if len(list) != 1 || list[0].Status != "failed" || list[0].ErrorCode != "model_start_failed" || j.loadStartedAt.IsZero() || j.readyAt.IsZero() {
		t.Fatalf("history: %+v job=%+v", list, j)
	}
}

func TestMonitorSelfCheckAndReportEndpoints(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "Qwen3-0.6B-Q8_0.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.env")
	if err := os.WriteFile(cfgPath, []byte("MODEL_PATH="+model+"\nMODEL_DIR="+dir+"\nLLM_PROVIDER=local\nSECRET_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cpuInfoPath = filepath.Join(dir, "cpuinfo")
	_ = os.WriteFile(cpuInfoPath, []byte("model name\t: Intel(R) Celeron(R) CPU J3455 @ 1.50GHz\nflags\t\t: fpu sse sse2 ssse3 sse4_1 sse4_2 aes\n"), 0o644)
	llamaLogPath = filepath.Join(dir, "llama.log")
	adminLogPath = filepath.Join(dir, "admin.log")
	_ = os.WriteFile(llamaLogPath, []byte(sampleLlamaLog+"\nAuthorization: Bearer secret-value\n"), 0o644)
	recentJobs = &jobHistory{}
	recentJobs.add(jobHistoryEntry{Status: "timed_out", ErrorCode: "generation_first_token_timeout"})
	m := &manager{configPath: cfgPath, qpkgDir: dir}
	versionOnce.Do(func() { versionCached = "0.0.0-test" })

	rec := httptest.NewRecorder()
	m.handleMonitor(rec, httptest.NewRequest(http.MethodGet, "/api/monitor", nil))
	var mon map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatal(err)
	}
	if mon["llama_log"].(map[string]any)["prompt_progress"].(float64) != 0.36 || len(mon["recent_jobs"].([]any)) != 1 {
		t.Fatalf("monitor: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	m.handleSelfCheck(rec, httptest.NewRequest(http.MethodGet, "/api/debug/selfcheck", nil))
	var sc struct{ Checks []checkResult }
	_ = json.Unmarshal(rec.Body.Bytes(), &sc)
	byID := map[string]checkResult{}
	for _, c := range sc.Checks {
		byID[c.ID] = c
	}
	if byID["cpu"].Status != "info" || !strings.Contains(byID["cpu"].Detail, "AVXなし") {
		t.Fatalf("cpu check: %+v", byID["cpu"])
	}
	if byID["model"].Status != "warn" || !strings.Contains(byID["model"].Hint, "Q4_0") {
		t.Fatalf("model check: %+v", byID["model"])
	}
	if byID["runtime"].Status != "fail" || byID["jobs"].Status != "warn" || !strings.Contains(byID["jobs"].Hint, "読み込み") {
		t.Fatalf("runtime/jobs: %+v %+v", byID["runtime"], byID["jobs"])
	}

	rec = httptest.NewRecorder()
	m.handleDebugReport(rec, httptest.NewRequest(http.MethodGet, "/api/debug/report?download=1", nil))
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "qnapassistant-debug-") ||
		strings.Contains(body, "secret-value") || strings.Contains(body, `"SECRET_TOKEN": "abc"`) || !strings.Contains(body, "Q8_0") {
		t.Fatalf("report: %s", body)
	}
}
