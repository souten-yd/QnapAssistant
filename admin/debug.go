package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Debug helpers: a self-check that explains common problems in plain words, and
// a downloadable report for support. Neither includes prompts, outputs, job IDs
// or secrets.

var cpuInfoPath = "/proc/cpuinfo"

var versionOnce sync.Once
var versionCached string

func (m *manager) cachedVersion() string {
	versionOnce.Do(func() { versionCached = m.currentQPKGVersion() })
	return versionCached
}

type cpuInfo struct {
	Model string   `json:"model"`
	Cores int      `json:"logical_cpus"`
	Flags []string `json:"simd_flags"`
}

func readCPUInfo() cpuInfo {
	info := cpuInfo{Cores: runtime.NumCPU()}
	f, err := os.Open(cpuInfoPath)
	if err != nil {
		return info
	}
	defer f.Close()
	want := map[string]bool{"sse4_1": true, "ssse3": true, "sse4_2": true, "avx": true, "avx2": true, "fma": true, "f16c": true, "avx512f": true}
	seen := map[string]bool{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "model name" && info.Model == "" {
			info.Model = v
		}
		if k == "flags" && len(seen) == 0 {
			for _, fl := range strings.Fields(v) {
				if want[fl] {
					seen[fl] = true
				}
			}
		}
	}
	for fl := range seen {
		info.Flags = append(info.Flags, fl)
	}
	sort.Strings(info.Flags)
	return info
}

func (c cpuInfo) has(flag string) bool {
	for _, f := range c.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

var reQuant = regexp.MustCompile(`(?i)[-_.](IQ\d_[A-Z]+|Q\d_K(?:_[SML])?|Q\d_\d|F16|BF16|F32)(?:\.gguf)?$`)

func quantOf(model string) string {
	if m := reQuant.FindStringSubmatch(strings.TrimSuffix(baseName(model), ".gguf") + ".gguf"); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

func diskFree(path string) (free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

type checkResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail, info
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// runSelfCheck performs only local, read-only checks (plus a loopback health probe).
func (m *manager) runSelfCheck() []checkResult {
	var out []checkResult
	add := func(id, name, status, detail, hint string) {
		out = append(out, checkResult{id, name, status, detail, hint})
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		add("config", "設定ファイル", "fail", "読み込めません: "+err.Error(), "QnapAssistantを再インストールするか、設定ファイルの権限を確認してください。")
		cfg = config{}
	} else {
		add("config", "設定ファイル", "ok", m.configPath, "")
	}
	cfg = defaults(cfg)
	provider := normalizedLLMProvider(cfg)
	add("provider", "AIの接続先", "info", map[string]string{"local": "ローカル（NAS内のllama.cpp）", "openrouter": "OpenRouter（外部）"}[provider], "")

	cpu := readCPUInfo()
	switch {
	case len(cpu.Flags) == 0:
		add("cpu", "CPU", "info", "CPU情報を取得できませんでした", "")
	case !cpu.has("ssse3") || !cpu.has("sse4_2"):
		add("cpu", "CPU", "fail", cpu.Model+"（SSE4.2/SSSE3なし）", "同梱のllama.cppはSSE4.2が必要です。このCPUでは動作しません。")
	case !cpu.has("avx"):
		add("cpu", "CPU", "info", fmt.Sprintf("%s・%dスレッド・AVXなし", cpu.Model, cpu.Cores),
			"AVXなしCPUでは Q4_0・Q4_K・Q6_K（Q4_K_M）が高速です。Q8_0・Q5系・IQ系は低速になります。")
	default:
		add("cpu", "CPU", "ok", fmt.Sprintf("%s・%dスレッド（%s）", cpu.Model, cpu.Cores, strings.Join(cpu.Flags, ", ")), "")
	}

	server := filepath.Join(m.qpkgDir, "bin", "llama-server")
	if st, err := os.Stat(server); err != nil || st.Mode()&0o111 == 0 {
		add("runtime", "llama.cpp 実行ファイル", "fail", server+" がありません", "QnapAssistantを再インストールしてください。")
	} else {
		add("runtime", "llama.cpp 実行ファイル", "ok", server, "")
	}

	_, memAvail, _ := systemStats()
	if provider == "local" {
		model := cfg["MODEL_PATH"]
		st, err := os.Stat(model)
		if err != nil {
			add("model", "モデルファイル", "fail", baseName(model)+" が見つかりません", "「AIモデル」タブでモデルをダウンロードし、選択してください。")
		} else {
			q := quantOf(model)
			detail := fmt.Sprintf("%s（%s%s）", baseName(model), gib(st.Size()), map[bool]string{true: "・" + q, false: ""}[q != ""])
			status, hint := "ok", ""
			if !cpu.has("avx") && len(cpu.Flags) > 0 && q != "" && !strings.HasPrefix(q, "Q4_0") && !strings.HasPrefix(q, "Q4_K") && !strings.HasPrefix(q, "Q6_K") {
				status, hint = "warn", "このCPUでは"+q+"は高速化の対象外です。Q4_0 か Q4_K_M のモデルを推奨します。"
			}
			add("model", "モデルファイル", status, detail, hint)
			ctx := intVal(cfg, "CONTEXT", 4096)
			if memAvail > 0 {
				need := st.Size() + int64(ctx)*256*1024 // weights + rough KV/compute allowance
				if running := m.backendLoaded(); !running && memAvail < need {
					add("memory", "空きメモリ", "warn", fmt.Sprintf("空き %s／必要の目安 %s", gib(memAvail), gib(need)),
						"他のアプリを止めるか、小さいモデル・短いCONTEXTにしてください。")
				} else {
					add("memory", "空きメモリ", "ok", "空き "+gib(memAvail), "")
				}
			}
		}
		if free, ok := diskFree(filepath.Dir(model)); ok {
			if free < 2<<30 {
				add("disk", "モデル保存先の空き容量", "warn", gib(free), "モデルのダウンロードやログ出力に失敗する可能性があります。")
			} else {
				add("disk", "モデル保存先の空き容量", "ok", gib(free), "")
			}
		}
		hStatus, hDetail, hHint := m.backendHealth(cfg)
		add("backend", "ローカルAIの応答", hStatus, hDetail, hHint)
	} else {
		key, _ := m.readOpenRouterKey()
		if key == "" {
			add("openrouter", "OpenRouter APIキー", "fail", "未設定", "「OpenRouter」タブでAPIキーを保存してください。")
		} else {
			add("openrouter", "OpenRouter APIキー", "ok", "設定済み（"+openRouterKeyFingerprint(key)+"）", "")
		}
	}

	failed := map[string]int{}
	for _, e := range recentJobs.list() {
		if e.Status != "completed" {
			failed[e.ErrorCode]++
		}
	}
	if len(failed) == 0 {
		add("jobs", "最近のAI処理", "ok", "失敗はありません", "")
	} else {
		var parts []string
		for code, n := range failed {
			parts = append(parts, fmt.Sprintf("%s×%d", code, n))
		}
		sort.Strings(parts)
		hint := "「診断」タブの処理履歴で詳細を確認してください。"
		if failed["generation_first_token_timeout"] > 0 {
			hint = "入力の読み込みが時間内に終わっていません。小さい・高速な量子化のモデルにするか、生成タイムアウトを延ばしてください。"
		}
		add("jobs", "最近のAI処理", "warn", "失敗: "+strings.Join(parts, ", "), hint)
	}
	return out
}

func (m *manager) backendLoaded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backendRunningLocked()
}

// backendHealth returns (status, detail, hint) for the local llama-server.
func (m *manager) backendHealth(cfg config) (string, string, string) {
	if !m.backendLoaded() {
		if llmAutoUnload(cfg) {
			return "info", "停止中（依頼が来ると自動で起動します）", ""
		}
		return "warn", "停止中", "ダッシュボードの「起動」で読み込めます。"
	}
	port, err := strconv.Atoi(cfg["BACKEND_PORT"])
	if err != nil || port <= 0 || port > 65535 {
		return "fail", "BACKEND_PORT が不正です", ""
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", cfg["BACKEND_PORT"]) + "/health")
	if err != nil {
		return "fail", "応答なし", "「診断」タブのllamaログを確認し、再起動してください。"
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return "ok", "準備完了", ""
	case http.StatusServiceUnavailable:
		return "info", "モデル読み込み中", ""
	}
	return "warn", fmt.Sprintf("HTTP %d", resp.StatusCode), ""
}

func (m *manager) handleSelfCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"checks": m.runSelfCheck(), "checked_at_unix_ms": time.Now().UnixMilli()})
}

var secretKey = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASS|AUTH)`)
var secretValue = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}|Bearer\s+\S+`)

func redactConfig(c config) map[string]string {
	out := map[string]string{}
	for k, v := range c {
		if secretKey.MatchString(k) && v != "" {
			v = "(redacted)"
		}
		out[k] = secretValue.ReplaceAllString(v, "(redacted)")
	}
	return out
}

func redactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = secretValue.ReplaceAllString(l, "(redacted)")
	}
	return out
}

// GET /api/debug/report: a JSON bundle to save and share when asking for help.
func (m *manager) handleDebugReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	cfg, _ := loadConfig(m.configPath)
	cfg = defaults(cfg)
	llamaLines, _ := tailLines(llamaLogPath, 200)
	adminLines, _ := tailLines(adminLogPath, 200)
	memTotal, memAvail, _ := systemStats()
	swapTotal, swapFree := swapStats()
	var models []map[string]any
	if entries, err := os.ReadDir(cfg["MODEL_DIR"]); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".gguf") {
				models = append(models, map[string]any{"name": e.Name(), "bytes": info.Size(), "quant": quantOf(e.Name())})
			}
		}
	}
	name := "qnapassistant-debug-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	writeJSON(w, map[string]any{
		"report_version": 1, "generated_at": time.Now().Format(time.RFC3339),
		"version": m.cachedVersion(), "go": runtime.Version(), "manager_uptime_s": seconds(processStarted, time.Now()),
		"cpu": readCPUInfo(), "load": loadAverages(),
		"memory":            map[string]int64{"total": memTotal, "available": memAvail, "swap_total": swapTotal, "swap_free": swapFree},
		"config":            redactConfig(cfg),
		"models":            models,
		"selfcheck":         m.runSelfCheck(),
		"jobs":              m.activeJobSummary(),
		"recent_jobs":       recentJobs.list(),
		"llama_log_summary": parseLlamaLog(llamaLines),
		"llama_log_tail":    redactLines(llamaLines),
		"admin_log_tail":    redactLines(adminLines),
	})
}
