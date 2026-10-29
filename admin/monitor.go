package main

import (
	"bufio"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Monitoring for the dashboard. Everything here is read-only and cheap enough
// to poll every few seconds: /proc reads, an in-memory job history and the
// tail of the llama-server log. Prompts, outputs and job IDs (bearer
// capabilities) are never included.

var llamaLogPath = "/share/Public/QnapAssistant/llama-server.log"
var adminLogPath = "/share/Public/QnapAssistant/admin.log"

const jobHistoryLimit = 30

var processStarted = time.Now()

// jobHistoryEntry is a privacy-safe summary of a finished local LLM job.
type jobHistoryEntry struct {
	FinishedUnixMs    int64   `json:"finished_unix_ms"`
	Status            string  `json:"status"`
	ErrorCode         string  `json:"error_code,omitempty"`
	Model             string  `json:"model"`
	TotalSeconds      float64 `json:"total_seconds"`
	QueueSeconds      float64 `json:"queue_seconds"`
	LoadSeconds       float64 `json:"load_seconds"`
	FirstTokenSeconds float64 `json:"first_token_seconds"` // prompt reading until the first output token; -1 when none
	GenerateSeconds   float64 `json:"generate_seconds"`
	PromptTokens      int     `json:"prompt_tokens"`
	CompletionTokens  int     `json:"completion_tokens"`
	TokensPerSecond   float64 `json:"tokens_per_second"`
	InputBytes        int     `json:"input_bytes"`
}

type jobHistory struct {
	mu      sync.Mutex
	entries []jobHistoryEntry
}

var recentJobs = &jobHistory{}

func (h *jobHistory) add(e jobHistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, e)
	if len(h.entries) > jobHistoryLimit {
		h.entries = append([]jobHistoryEntry(nil), h.entries[len(h.entries)-jobHistoryLimit:]...)
	}
}

// list returns the newest first.
func (h *jobHistory) list() []jobHistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]jobHistoryEntry, len(h.entries))
	for i, e := range h.entries {
		out[len(h.entries)-1-i] = e
	}
	return out
}

func seconds(from, to time.Time) float64 {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return float64(to.Sub(from).Milliseconds()) / 1000
}

// summarizeJob builds the history entry. Called with jobsMu held, once, when the job ends.
func summarizeJob(j *llmJob) jobHistoryEntry {
	end := j.finished
	if end.IsZero() {
		end = time.Now()
	}
	e := jobHistoryEntry{
		FinishedUnixMs: end.UnixMilli(), Status: j.state, ErrorCode: j.errorCode,
		Model: baseName(j.model), TotalSeconds: seconds(j.created, end),
		PromptTokens: j.promptTokens, CompletionTokens: j.completionTokens, InputBytes: j.inputBytes,
		FirstTokenSeconds: -1,
	}
	if !j.loadStartedAt.IsZero() {
		e.QueueSeconds = seconds(j.created, j.loadStartedAt)
		loadEnd := j.readyAt
		if loadEnd.IsZero() {
			loadEnd = end
		}
		e.LoadSeconds = seconds(j.loadStartedAt, loadEnd)
	}
	if !j.generationStarted.IsZero() {
		e.GenerateSeconds = seconds(j.generationStarted, end)
		if !j.firstTokenAt.IsZero() {
			e.FirstTokenSeconds = seconds(j.generationStarted, j.firstTokenAt)
			if gen := seconds(j.firstTokenAt, end); gen > 0 && j.completionTokens > 0 {
				e.TokensPerSecond = float64(int(float64(j.completionTokens)/gen*100)) / 100
			}
		}
	}
	return e
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ---- llama-server log parsing: live prompt-reading progress and last timings ----

var (
	reProgress   = regexp.MustCompile(`prompt processing, n_tokens =\s*(\d+), progress =\s*([0-9.]+), t =\s*([0-9.]+) s /\s*([0-9.]+) tokens per second`)
	rePromptEval = regexp.MustCompile(`prompt eval time =\s*([0-9.]+) ms /\s*(\d+) tokens \(\s*[0-9.]+ ms per token,\s*([0-9.]+) tokens per second\)`)
	reEval       = regexp.MustCompile(`\beval time =\s*([0-9.]+) ms /\s*(\d+) tokens \(\s*[0-9.]+ ms per token,\s*([0-9.]+) tokens per second\)`)
	reLaunch     = regexp.MustCompile(`launch_slot_: .*processing task`)
	reRelease    = regexp.MustCompile(`slot\s+release: .*stop processing`)
	reModelLoad  = regexp.MustCompile(`load_model: loading model '([^']+)'`)
)

type llamaLogSummary struct {
	Processing          bool    `json:"processing"`            // a task started and has not been released yet
	PromptProgress      float64 `json:"prompt_progress"`       // 0..1 while reading the prompt, -1 when unknown
	PromptTokensDone    int     `json:"prompt_tokens_done"`    // tokens read so far
	PromptTokensTotal   int     `json:"prompt_tokens_total"`   // estimated from progress
	PromptTokensPerSec  float64 `json:"prompt_tokens_per_sec"` // live or last measured prompt speed
	GenTokensPerSec     float64 `json:"gen_tokens_per_sec"`    // last measured generation speed
	LastPromptTokens    int     `json:"last_prompt_tokens"`
	LastGeneratedTokens int     `json:"last_generated_tokens"`
	LoadedModel         string  `json:"loaded_model,omitempty"`
}

// parseLlamaLog reads the newest lines first and stops once everything is known.
func parseLlamaLog(lines []string) llamaLogSummary {
	s := llamaLogSummary{PromptProgress: -1}
	sawTaskBoundary, sawPrompt, sawEval := false, false, false
	var progress []string // newest progress line of the current task, if any
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !sawTaskBoundary {
			if reRelease.MatchString(l) {
				sawTaskBoundary = true
			} else if reLaunch.MatchString(l) {
				sawTaskBoundary, s.Processing = true, true
			} else if progress == nil {
				progress = reProgress.FindStringSubmatch(l)
			}
		}
		if !sawPrompt {
			if m := rePromptEval.FindStringSubmatch(l); m != nil {
				sawPrompt = true
				s.LastPromptTokens, _ = strconv.Atoi(m[2])
				s.PromptTokensPerSec, _ = strconv.ParseFloat(m[3], 64)
			}
		}
		if !sawEval && !strings.Contains(l, "prompt eval time") {
			if m := reEval.FindStringSubmatch(l); m != nil {
				sawEval = true
				s.LastGeneratedTokens, _ = strconv.Atoi(m[2])
				s.GenTokensPerSec, _ = strconv.ParseFloat(m[3], 64)
			}
		}
		if s.LoadedModel == "" {
			if m := reModelLoad.FindStringSubmatch(l); m != nil {
				s.LoadedModel = baseName(m[1])
			}
		}
		if sawTaskBoundary && sawPrompt && sawEval && s.LoadedModel != "" {
			break
		}
	}
	if s.Processing && progress != nil { // live prompt reading of the running task
		s.PromptTokensDone, _ = strconv.Atoi(progress[1])
		s.PromptProgress, _ = strconv.ParseFloat(progress[2], 64)
		s.PromptTokensPerSec, _ = strconv.ParseFloat(progress[4], 64)
		if s.PromptProgress > 0 {
			s.PromptTokensTotal = int(float64(s.PromptTokensDone)/s.PromptProgress + 0.5)
		}
	}
	return s
}

// ---- CPU usage from /proc/stat deltas ----

type cpuSampler struct {
	mu         sync.Mutex
	idle, all  uint64
	lastSample time.Time
	lastUsage  float64
}

var cpuUsage = &cpuSampler{lastUsage: -1}

func readCPUTimes() (idle, all uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(s.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		all += n
		if i == 3 || i == 4 { // idle + iowait
			idle += n
		}
	}
	return idle, all, true
}

// percent returns CPU usage (0-100) since the previous call, or -1 on the first call.
func (c *cpuSampler) percent() float64 {
	idle, all, ok := readCPUTimes()
	if !ok {
		return -1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.all != 0 && all > c.all {
		dAll, dIdle := float64(all-c.all), float64(idle-c.idle)
		c.lastUsage = float64(int((1-dIdle/dAll)*1000)) / 10
	}
	if c.all == 0 || time.Since(c.lastSample) > time.Second {
		c.idle, c.all, c.lastSample = idle, all, time.Now()
	}
	return c.lastUsage
}

func loadAverages() []float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(b))
	out := []float64{}
	for i := 0; i < 3 && i < len(fields); i++ {
		v, _ := strconv.ParseFloat(fields[i], 64)
		out = append(out, v)
	}
	return out
}

func swapStats() (total, free int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "SwapTotal:":
			total = v * 1024
		case "SwapFree:":
			free = v * 1024
		}
	}
	return
}

// activeJobSummary describes the job currently being processed, if any.
func (m *manager) activeJobSummary() map[string]any {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	var cur *llmJob
	queued := 0
	for _, j := range m.jobs {
		if j.state == "queued" {
			queued++
			continue
		}
		if !j.terminal() && (cur == nil || j.created.Before(cur.created)) {
			cur = j
		}
	}
	out := map[string]any{"queued": queued}
	if cur == nil {
		return out
	}
	now := time.Now()
	sinceToken := -1.0
	if !cur.lastTokenAt.IsZero() {
		sinceToken = seconds(cur.lastTokenAt, now)
	}
	out["current"] = map[string]any{
		"status": cur.state, "elapsed_seconds": seconds(cur.created, now),
		"stage_elapsed_seconds": seconds(cur.stageAt, now), "model": baseName(cur.model),
		"input_bytes": cur.inputBytes, "token_chunks": cur.tokenChunks,
		"since_last_token_seconds": sinceToken,
	}
	return out
}

// GET /api/monitor: one call for the dashboard.
func (m *manager) handleMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	provider := normalizedLLMProvider(cfg)
	m.mu.Lock()
	running := m.backendRunningLocked()
	active := m.activeRequests
	started := m.startedAt
	lastUsed := m.lastUsed
	m.mu.Unlock()
	memTotal, memAvail, _ := systemStats()
	swapTotal, swapFree := swapStats()
	lines, _ := tailLines(llamaLogPath, 400)
	llama := parseLlamaLog(lines)
	backendUptime := 0.0
	if running {
		backendUptime = seconds(started, time.Now())
	}
	idle := 0.0
	if active == 0 && !lastUsed.IsZero() {
		idle = seconds(lastUsed, time.Now())
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"version":          m.cachedVersion(),
		"manager_uptime_s": seconds(processStarted, time.Now()),
		"system": map[string]any{
			"cpu_count": runtime.NumCPU(), "cpu_percent": cpuUsage.percent(), "load": loadAverages(),
			"memory_total_bytes": memTotal, "memory_available_bytes": memAvail,
			"swap_total_bytes": swapTotal, "swap_free_bytes": swapFree,
		},
		"llm": map[string]any{
			"provider": provider, "model": baseName(cfg["MODEL_PATH"]), "context_size": intVal(cfg, "CONTEXT", 4096),
			"threads": cfg["THREADS"], "backend_process_alive": running, "backend_uptime_s": backendUptime,
			"active_requests": active, "idle_seconds": idle, "auto_unload": llmAutoUnload(cfg),
			"idle_timeout_seconds": llmIdleTimeout(cfg), "openrouter_model": cfg["OPENROUTER_MODEL"],
		},
		"llama_log":          llama,
		"jobs":               m.activeJobSummary(),
		"recent_jobs":        recentJobs.list(),
		"sampled_at_unix_ms": time.Now().UnixMilli(),
	})
}
