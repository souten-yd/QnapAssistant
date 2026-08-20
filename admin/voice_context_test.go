package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"testing"
)

func ptrString(v string) *string { return &v }
func ptrInt(v int) *int          { return &v }

func TestVoiceContextHeaderControlsPayload(t *testing.T) {
	context := voiceChatControls{
		System:    ptrString("あなたはM5GOの相棒です。短く答えてください。"),
		MaxTokens: ptrInt(37),
		History: []voiceChatMessage{
			{Role: "user", Content: "前の質問"},
			{Role: "assistant", Content: "前の回答"},
		},
	}
	b, _ := json.Marshal(context)
	header := base64.RawURLEncoding.EncodeToString(b)
	r := httptest.NewRequest("POST", "/v1/voice/chat/stream?profile=m5go", bytes.NewReader([]byte{1, 2, 3, 4}))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("X-Sample-Rate", "16000")
	r.Header.Set("X-Qnap-Voice-Context", header)

	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.SampleRate != "16000" || !bytes.Equal(input.Audio, []byte{1, 2, 3, 4}) {
		t.Fatalf("unexpected audio input: rate=%q audio=%v", input.SampleRate, input.Audio)
	}
	if input.Controls.System == nil || *input.Controls.System != *context.System {
		t.Fatalf("system override lost: %#v", input.Controls.System)
	}
	if got := voiceEffectiveMaxTokens(config{}, input.Controls); got != 37 {
		t.Fatalf("max_tokens=%d want 37", got)
	}

	msgs := voiceLLMMessages(config{}, "現在の質問", input.Controls)
	want := []map[string]string{
		{"role": "system", "content": *context.System},
		{"role": "user", "content": "前の質問"},
		{"role": "assistant", "content": "前の回答"},
		{"role": "user", "content": "現在の質問"},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("messages=%#v want %#v", msgs, want)
	}
}

func TestVoiceQueryControlsAreApplied(t *testing.T) {
	history := `[{"role":"user","content":"old question"},{"role":"assistant","content":"old answer"}]`
	r := httptest.NewRequest("POST", "/v1/voice/chat?system=custom+voice&max_tokens=600&history="+urlQueryEscape(history), bytes.NewReader([]byte{1}))
	r.Header.Set("Content-Type", "audio/wav")
	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.Controls.System == nil || *input.Controls.System != "custom voice" {
		t.Fatalf("system query not applied: %#v", input.Controls.System)
	}
	if input.Controls.MaxTokens == nil || *input.Controls.MaxTokens != 600 {
		t.Fatalf("max_tokens query not applied: %#v", input.Controls.MaxTokens)
	}
	if len(input.Controls.History) != 2 {
		t.Fatalf("history query not applied: %#v", input.Controls.History)
	}
}

func TestVoiceContextHeaderOverridesQuery(t *testing.T) {
	ctx := voiceChatControls{System: ptrString("header"), MaxTokens: ptrInt(21)}
	b, _ := json.Marshal(ctx)
	r := httptest.NewRequest("POST", "/v1/voice/chat?system=query&max_tokens=99", bytes.NewReader([]byte{1}))
	r.Header.Set("Content-Type", "audio/wav")
	r.Header.Set("X-Qnap-Voice-Context", base64.RawURLEncoding.EncodeToString(b))
	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if *input.Controls.System != "header" || *input.Controls.MaxTokens != 21 {
		t.Fatalf("header should override query: %#v", input.Controls)
	}
}

func TestVoiceContextExplicitEmptySystem(t *testing.T) {
	controls := voiceChatControls{System: ptrString("")}
	msgs := voiceLLMMessages(config{}, "hello", controls)
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("explicit empty system should remove system message: %#v", msgs)
	}
}

func TestVoiceContextMessagesAlias(t *testing.T) {
	controls := voiceChatControls{Messages: []voiceChatMessage{{Role: "USER", Content: " before "}}}
	if err := normalizeVoiceControls(&controls); err != nil {
		t.Fatal(err)
	}
	if len(controls.History) != 1 || controls.History[0].Role != "user" || controls.History[0].Content != "before" {
		t.Fatalf("messages alias was not normalized: %#v", controls)
	}
}

func TestVoiceContextRejectsInvalidHistoryRole(t *testing.T) {
	controls := voiceChatControls{History: []voiceChatMessage{{Role: "system", Content: "not history"}}}
	if err := normalizeVoiceControls(&controls); err == nil {
		t.Fatal("expected invalid history role to fail")
	}
}

func TestVoiceMultipartContextAndAudio(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	ctxHeader := make(textproto.MIMEHeader)
	ctxHeader.Set("Content-Disposition", `form-data; name="context"`)
	ctxHeader.Set("Content-Type", "application/json")
	ctxPart, err := mw.CreatePart(ctxHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ctxPart.Write([]byte(`{"system":"multipart system","max_tokens":23,"history":[{"role":"user","content":"old"}]}`))

	audioHeader := make(textproto.MIMEHeader)
	audioHeader.Set("Content-Disposition", `form-data; name="audio"; filename="speech.pcm"`)
	audioHeader.Set("Content-Type", "application/octet-stream")
	audioHeader.Set("X-Sample-Rate", "16000")
	audioPart, err := mw.CreatePart(audioHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = audioPart.Write([]byte{9, 8, 7, 6})
	_ = mw.Close()

	r := httptest.NewRequest("POST", "/v1/voice/chat", bytes.NewReader(body.Bytes()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	input, err := parseVoiceChatInput(r)
	if err != nil {
		t.Fatal(err)
	}
	if input.SampleRate != "16000" || !bytes.Equal(input.Audio, []byte{9, 8, 7, 6}) {
		t.Fatalf("unexpected multipart audio: rate=%q audio=%v", input.SampleRate, input.Audio)
	}
	if input.Controls.System == nil || *input.Controls.System != "multipart system" {
		t.Fatalf("multipart context not applied: %#v", input.Controls)
	}
	if input.Controls.MaxTokens == nil || *input.Controls.MaxTokens != 23 || len(input.Controls.History) != 1 {
		t.Fatalf("multipart controls incomplete: %#v", input.Controls)
	}
}

func TestVoiceContextRejectsHugeTokenLimit(t *testing.T) {
	controls := voiceChatControls{MaxTokens: ptrInt(voiceMaxReplyTokens + 1)}
	if err := normalizeVoiceControls(&controls); err == nil {
		t.Fatal("expected max_tokens bound error")
	}
}

func urlQueryEscape(value string) string {
	// Keep tests independent of a browser/client implementation while still
	// exercising net/url decoding through httptest.NewRequest.
	replacer := map[byte]string{' ': "+", '[': "%5B", ']': "%5D", '{': "%7B", '}': "%7D", '"': "%22", ':': "%3A", ',': "%2C"}
	var out bytes.Buffer
	for i := 0; i < len(value); i++ {
		if replacement, ok := replacer[value[i]]; ok {
			out.WriteString(replacement)
		} else {
			out.WriteByte(value[i])
		}
	}
	return out.String()
}
