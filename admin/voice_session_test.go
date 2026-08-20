package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func ptrBool(v bool) *bool { return &v }

func TestVoiceDefaultOmitsMaxTokens(t *testing.T) {
	cfg := defaults(config{})
	payload := voiceLLMPayloadStandard(cfg, "質問です", true, voiceChatControls{})
	if _, ok := payload["max_tokens"]; ok {
		t.Fatalf("default voice payload must inherit backend max_tokens: %#v", payload["max_tokens"])
	}
	if got := voiceMaxTokensTelemetry(cfg, voiceChatControls{}); got != "backend_default" {
		t.Fatalf("telemetry=%v want backend_default", got)
	}
}

func TestVoiceExplicitMaxTokensStillWorks(t *testing.T) {
	controls := voiceChatControls{MaxTokens: ptrInt(600)}
	payload := voiceLLMPayloadStandard(defaults(config{}), "質問です", true, controls)
	if got, ok := payload["max_tokens"].(int); !ok || got != 600 {
		t.Fatalf("explicit max_tokens was not forwarded: %#v", payload["max_tokens"])
	}
}

func TestVoiceConfiguredMaxTokensStillWorks(t *testing.T) {
	cfg := defaults(config{"VOICE_REPLY_MAX_TOKENS": "512"})
	payload := voiceLLMPayloadStandard(cfg, "質問です", true, voiceChatControls{})
	if got, ok := payload["max_tokens"].(int); !ok || got != 512 {
		t.Fatalf("configured max_tokens was not forwarded: %#v", payload["max_tokens"])
	}
}

func TestVoiceMultipartSessionControls(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	ctxHeader := make(textproto.MIMEHeader)
	ctxHeader.Set("Content-Disposition", `form-data; name="context"`)
	ctxHeader.Set("Content-Type", "application/json")
	ctx, err := mw.CreatePart(ctxHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ctx.Write([]byte(`{"session_id":"m5-room","reset_session":true}`))
	audioHeader := make(textproto.MIMEHeader)
	audioHeader.Set("Content-Disposition", `form-data; name="audio"; filename="speech.pcm"`)
	audioHeader.Set("Content-Type", "application/octet-stream")
	audio, err := mw.CreatePart(audioHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = audio.Write([]byte{1, 2, 3, 4})
	_ = mw.Close()

	r := httptest.NewRequest("POST", "/v1/voice/chat?session_id=query-session", bytes.NewReader(body.Bytes()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.Controls.SessionID == nil || *input.Controls.SessionID != "m5-room" {
		t.Fatalf("multipart session did not override query: %#v", input.Controls.SessionID)
	}
	if input.Controls.ResetSession == nil || !*input.Controls.ResetSession {
		t.Fatalf("multipart reset_session not preserved: %#v", input.Controls.ResetSession)
	}
}

func TestVoiceHeaderSessionControls(t *testing.T) {
	ctx := voiceChatControls{SessionID: ptrString("kizuna-room"), ResetSession: ptrBool(false)}
	b, _ := json.Marshal(ctx)
	r := httptest.NewRequest("POST", "/v1/voice/chat/stream?session_id=query", bytes.NewReader([]byte{1}))
	r.Header.Set("Content-Type", "audio/wav")
	r.Header.Set("X-Qnap-Voice-Context", base64.RawURLEncoding.EncodeToString(b))
	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.Controls.SessionID == nil || *input.Controls.SessionID != "kizuna-room" {
		t.Fatalf("header session did not override query: %#v", input.Controls.SessionID)
	}
}

func TestVoiceSessionCompactsHundredsOfTurns(t *testing.T) {
	s := newVoiceSession("many-turns")
	for i := 0; i < 200; i++ {
		s.Recent = append(s.Recent,
			voiceChatMessage{Role: "user", Content: "ユーザーの長い発話内容を保持します。" + strings.Repeat("あ", 20)},
			voiceChatMessage{Role: "assistant", Content: "アシスタントの回答内容を保持します。" + strings.Repeat("い", 20)},
		)
		s.TotalMessages += 2
		s.Turns++
		compactVoiceSession(&s)
	}
	if len(s.Recent) != voiceSessionRecentMessages {
		t.Fatalf("recent messages=%d want %d", len(s.Recent), voiceSessionRecentMessages)
	}
	if got := len([]rune(s.Summary)); got > voiceSessionSummaryRunes+2 {
		t.Fatalf("summary grew without bound: %d runes", got)
	}
	if s.TotalMessages != 400 || s.Turns != 200 {
		t.Fatalf("session counters lost: messages=%d turns=%d", s.TotalMessages, s.Turns)
	}
}
