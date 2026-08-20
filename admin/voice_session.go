package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	voiceSessionRecentMessages = 12
	voiceSessionSummaryRunes    = 1200
	voiceSessionLineRunes       = 180
	voiceSessionTTL             = 7 * 24 * time.Hour
	voiceSessionMaxIDBytes      = 64
)

type voiceSessionEnvelope struct {
	SessionID    string `json:"session_id,omitempty"`
	ResetSession bool   `json:"reset_session,omitempty"`
}

type voiceSessionState struct {
	ID            string             `json:"id"`
	Summary       string             `json:"summary,omitempty"`
	Recent        []voiceChatMessage `json:"recent,omitempty"`
	Turns         int                `json:"turns"`
	TotalMessages int                `json:"total_messages"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type voiceSessionStats struct {
	ID              string
	Turns           int
	RecentMessages  int
	SummaryRunes    int
	TotalMessages   int
}

type voiceSessionUse struct {
	cfg   config
	state voiceSessionState
	mu    *sync.Mutex
	done  bool
}

var voiceSessionLocks sync.Map

func voiceSessionMutex(id string) *sync.Mutex {
	if v, ok := voiceSessionLocks.Load(id); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := voiceSessionLocks.LoadOrStore(id, mu)
	return actual.(*sync.Mutex)
}

func validVoiceSessionID(id string) bool {
	if id == "" || len(id) > voiceSessionMaxIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func voiceBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func requestedVoiceSession(r *http.Request) (voiceSessionEnvelope, error) {
	env := voiceSessionEnvelope{SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")), ResetSession: voiceBool(r.URL.Query().Get("reset_session"))}
	if raw := strings.TrimSpace(r.Header.Get("X-Qnap-Voice-Context")); raw != "" {
		decoded, err := decodeBase64URLOrStd(raw)
		if err != nil {
			return env, fmt.Errorf("decode session context: %w", err)
		}
		var header voiceSessionEnvelope
		if err := json.Unmarshal(decoded, &header); err != nil {
			return env, fmt.Errorf("decode session context JSON: %w", err)
		}
		if strings.TrimSpace(header.SessionID) != "" {
			env.SessionID = strings.TrimSpace(header.SessionID)
		}
		if header.ResetSession {
			env.ResetSession = true
		}
	}
	if env.SessionID != "" && !validVoiceSessionID(env.SessionID) {
		return env, fmt.Errorf("session_id must be 1-%d ASCII letters, digits, '.', '_' or '-'", voiceSessionMaxIDBytes)
	}
	return env, nil
}

func voiceSessionDir(cfg config) string {
	return filepath.Join(get(cfg, "VOICE_DIR", "/share/Public/QnapAssistant/voice"), "sessions")
}

func voiceSessionPath(cfg config, id string) string {
	return filepath.Join(voiceSessionDir(cfg), id+".json")
}

func newVoiceSession(id string) voiceSessionState {
	now := time.Now()
	return voiceSessionState{ID: id, CreatedAt: now, UpdatedAt: now}
}

func loadVoiceSession(cfg config, id string, reset bool) (voiceSessionState, error) {
	path := voiceSessionPath(cfg, id)
	if reset {
		_ = os.Remove(path)
		return newVoiceSession(id), nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newVoiceSession(id), nil
	}
	if err != nil {
		return voiceSessionState{}, err
	}
	var s voiceSessionState
	if err := json.Unmarshal(b, &s); err != nil {
		return voiceSessionState{}, fmt.Errorf("decode voice session %s: %w", id, err)
	}
	if s.ID != id {
		return voiceSessionState{}, fmt.Errorf("voice session id mismatch")
	}
	if !s.UpdatedAt.IsZero() && time.Since(s.UpdatedAt) > voiceSessionTTL {
		return newVoiceSession(id), nil
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	return s, nil
}

func saveVoiceSession(cfg config, s voiceSessionState) error {
	dir := voiceSessionDir(cfg)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	s.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := voiceSessionPath(cfg, s.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	if n < 2 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func compactVoiceMemoryLine(m voiceChatMessage) string {
	prefix := "U"
	if m.Role == "assistant" {
		prefix = "A"
	}
	text := strings.Join(strings.Fields(m.Content), " ")
	return prefix + ": " + truncateRunes(text, voiceSessionLineRunes)
}

func trimVoiceSummary(summary string) string {
	r := []rune(strings.TrimSpace(summary))
	if len(r) <= voiceSessionSummaryRunes {
		return string(r)
	}
	r = r[len(r)-voiceSessionSummaryRunes:]
	out := string(r)
	if idx := strings.IndexByte(out, '\n'); idx >= 0 && idx+1 < len(out) {
		out = out[idx+1:]
	}
	return "…\n" + strings.TrimSpace(out)
}

func compactVoiceSession(s *voiceSessionState) {
	for len(s.Recent) > voiceSessionRecentMessages {
		moved := s.Recent[0]
		s.Recent = append([]voiceChatMessage(nil), s.Recent[1:]...)
		line := compactVoiceMemoryLine(moved)
		if strings.TrimSpace(s.Summary) == "" {
			s.Summary = line
		} else {
			s.Summary += "\n" + line
		}
		s.Summary = trimVoiceSummary(s.Summary)
	}
}

func voiceSessionStatsOf(s voiceSessionState) voiceSessionStats {
	return voiceSessionStats{ID: s.ID, Turns: s.Turns, RecentMessages: len(s.Recent), SummaryRunes: utf8.RuneCountInString(s.Summary), TotalMessages: s.TotalMessages}
}

func mergeVoiceSessionControls(cfg config, s voiceSessionState, controls voiceChatControls) voiceChatControls {
	effective := controls
	history := make([]voiceChatMessage, 0, len(s.Recent)+len(controls.History))
	history = append(history, s.Recent...)
	history = append(history, controls.History...)
	if len(history) > voiceMaxHistoryItems {
		history = append([]voiceChatMessage(nil), history[len(history)-voiceMaxHistoryItems:]...)
	}
	effective.History = history

	if strings.TrimSpace(s.Summary) != "" {
		base := get(cfg, "VOICE_SYSTEM_PROMPT", "あなたは音声アシスタントです。ユーザーの発話内容を踏まえて自然な日本語で答えてください。入力内容をそのまま繰り返すだけの返答を避け、質問や依頼に直接答えてください。説明が必要な場合は省略せず、内容に応じた必要十分な長さで回答してください。")
		if controls.System != nil {
			base = *controls.System
		}
		memory := "以下は同じ会話セッションの古い発話を圧縮したメモです。現在の質問に関係する場合だけ参照してください。推測で補完しないでください。\n[会話メモ]\n" + s.Summary
		if strings.TrimSpace(base) == "" {
			base = memory
		} else {
			base += "\n\n" + memory
		}
		effective.System = &base
	}
	return effective
}

func beginVoiceSession(r *http.Request, cfg config, controls voiceChatControls) (*voiceSessionUse, voiceChatControls, voiceSessionStats, error) {
	env, err := requestedVoiceSession(r)
	if err != nil {
		return nil, controls, voiceSessionStats{}, err
	}
	// parseVoiceChatInput has already applied transport precedence
	// (query < header < multipart). Let the normalized control object win so a
	// multipart context can select/reset a session exactly like the header form.
	if controls.SessionID != nil {
		env.SessionID = strings.TrimSpace(*controls.SessionID)
	}
	if controls.ResetSession != nil {
		env.ResetSession = *controls.ResetSession
	}
	if env.SessionID == "" {
		return nil, controls, voiceSessionStats{}, nil
	}
	if !validVoiceSessionID(env.SessionID) {
		return nil, controls, voiceSessionStats{}, fmt.Errorf("session_id must be 1-%d ASCII letters, digits, '.', '_' or '-'", voiceSessionMaxIDBytes)
	}
	mu := voiceSessionMutex(env.SessionID)
	mu.Lock()
	s, err := loadVoiceSession(cfg, env.SessionID, env.ResetSession)
	if err != nil {
		mu.Unlock()
		return nil, controls, voiceSessionStats{}, err
	}
	use := &voiceSessionUse{cfg: cfg, state: s, mu: mu}
	return use, mergeVoiceSessionControls(cfg, s, controls), voiceSessionStatsOf(s), nil
}

func (u *voiceSessionUse) Commit(transcript, reply string) (voiceSessionStats, error) {
	if u == nil {
		return voiceSessionStats{}, nil
	}
	if u.done {
		return voiceSessionStatsOf(u.state), nil
	}
	transcript = strings.TrimSpace(transcript)
	reply = strings.TrimSpace(reply)
	if transcript != "" {
		u.state.Recent = append(u.state.Recent, voiceChatMessage{Role: "user", Content: transcript})
		u.state.TotalMessages++
	}
	if reply != "" {
		u.state.Recent = append(u.state.Recent, voiceChatMessage{Role: "assistant", Content: reply})
		u.state.TotalMessages++
	}
	if transcript != "" || reply != "" {
		u.state.Turns++
	}
	compactVoiceSession(&u.state)
	err := saveVoiceSession(u.cfg, u.state)
	stats := voiceSessionStatsOf(u.state)
	u.done = true
	u.mu.Unlock()
	return stats, err
}

func (u *voiceSessionUse) Abort() {
	if u == nil || u.done {
		return
	}
	u.done = true
	u.mu.Unlock()
}

func addVoiceSessionTimings(dst map[string]any, stats voiceSessionStats) {
	if stats.ID == "" {
		return
	}
	dst["session_id"] = stats.ID
	dst["session_turns"] = stats.Turns
	dst["session_recent_messages"] = stats.RecentMessages
	dst["session_summary_chars"] = stats.SummaryRunes
	dst["session_total_messages"] = stats.TotalMessages
}
