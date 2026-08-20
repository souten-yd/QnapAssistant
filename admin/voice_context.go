package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

const (
	voiceMaxAudioBytes   = 32 << 20
	voiceMaxContextBytes = 64 << 10
	voiceMaxSystemBytes  = 8 << 10
	voiceMaxHistoryBytes = 48 << 10
	voiceMaxHistoryItems = 32
	voiceMaxReplyTokens  = 2048
)

type voiceChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type voiceChatControls struct {
	System    *string            `json:"system,omitempty"`
	MaxTokens *int               `json:"max_tokens,omitempty"`
	History   []voiceChatMessage `json:"history,omitempty"`
	// OpenAI-style alias. The server appends the current ASR transcript after
	// these messages, so callers should not include the current utterance here.
	Messages []voiceChatMessage `json:"messages,omitempty"`
}

type voiceChatInput struct {
	Audio            []byte
	AudioContentType string
	SampleRate       string
	Controls         voiceChatControls
}

func readVoiceBounded(r io.Reader, limit int64, label string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return b, nil
}

func mergeVoiceControls(dst *voiceChatControls, src voiceChatControls) {
	if src.System != nil {
		dst.System = src.System
	}
	if src.MaxTokens != nil {
		dst.MaxTokens = src.MaxTokens
	}
	if src.History != nil {
		dst.History = src.History
	}
	if src.Messages != nil {
		dst.Messages = src.Messages
	}
}

func decodeBase64URLOrStd(value string) ([]byte, error) {
	var last error
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		b, err := enc.DecodeString(value)
		if err == nil {
			return b, nil
		}
		last = err
	}
	return nil, last
}

func decodeVoiceContextHeader(value string) (voiceChatControls, error) {
	var controls voiceChatControls
	value = strings.TrimSpace(value)
	if value == "" {
		return controls, nil
	}
	if len(value) > voiceMaxContextBytes*2 {
		return controls, fmt.Errorf("X-Qnap-Voice-Context is too large")
	}
	decoded, err := decodeBase64URLOrStd(value)
	if err != nil {
		return controls, fmt.Errorf("decode X-Qnap-Voice-Context: %w", err)
	}
	if len(decoded) > voiceMaxContextBytes {
		return controls, fmt.Errorf("voice context exceeds %d bytes", voiceMaxContextBytes)
	}
	if err := json.Unmarshal(decoded, &controls); err != nil {
		return controls, fmt.Errorf("decode voice context JSON: %w", err)
	}
	return controls, nil
}

func decodeVoiceHistoryValue(value string) ([]voiceChatMessage, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var raw []byte
	if strings.HasPrefix(value, "[") {
		raw = []byte(value)
	} else {
		decoded, err := decodeBase64URLOrStd(value)
		if err != nil {
			return nil, fmt.Errorf("history must be JSON or base64url JSON: %w", err)
		}
		raw = decoded
	}
	if len(raw) > voiceMaxHistoryBytes {
		return nil, fmt.Errorf("history exceeds %d bytes", voiceMaxHistoryBytes)
	}
	var history []voiceChatMessage
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	return history, nil
}

func queryVoiceControls(r *http.Request) (voiceChatControls, error) {
	var controls voiceChatControls
	q := r.URL.Query()
	if values, ok := q["system"]; ok {
		s := ""
		if len(values) > 0 {
			s = values[len(values)-1]
		}
		controls.System = &s
	}
	if raw := strings.TrimSpace(q.Get("max_tokens")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return controls, fmt.Errorf("max_tokens must be an integer")
		}
		controls.MaxTokens = &n
	}
	if raw := strings.TrimSpace(q.Get("history")); raw != "" {
		history, err := decodeVoiceHistoryValue(raw)
		if err != nil {
			return controls, err
		}
		controls.History = history
	}
	return controls, nil
}

func normalizeVoiceControls(controls *voiceChatControls) error {
	if controls.History != nil && controls.Messages != nil {
		return fmt.Errorf("use either history or messages, not both")
	}
	if controls.History == nil && controls.Messages != nil {
		controls.History = controls.Messages
	}
	controls.Messages = nil

	if controls.System != nil && len(*controls.System) > voiceMaxSystemBytes {
		return fmt.Errorf("system exceeds %d bytes", voiceMaxSystemBytes)
	}
	if controls.MaxTokens != nil && (*controls.MaxTokens < 1 || *controls.MaxTokens > voiceMaxReplyTokens) {
		return fmt.Errorf("max_tokens must be between 1 and %d", voiceMaxReplyTokens)
	}
	if len(controls.History) > voiceMaxHistoryItems {
		return fmt.Errorf("history supports at most %d messages", voiceMaxHistoryItems)
	}
	total := 0
	for i := range controls.History {
		m := &controls.History[i]
		m.Role = strings.ToLower(strings.TrimSpace(m.Role))
		m.Content = strings.TrimSpace(m.Content)
		if m.Role != "user" && m.Role != "assistant" {
			return fmt.Errorf("history[%d].role must be user or assistant", i)
		}
		if m.Content == "" {
			return fmt.Errorf("history[%d].content is empty", i)
		}
		if len(m.Content) > voiceMaxSystemBytes {
			return fmt.Errorf("history[%d].content exceeds %d bytes", i, voiceMaxSystemBytes)
		}
		total += len(m.Content)
		if total > voiceMaxHistoryBytes {
			return fmt.Errorf("history exceeds %d bytes", voiceMaxHistoryBytes)
		}
	}
	return nil
}

func parseVoiceMultipart(r *http.Request, boundary string, controls voiceChatControls) (voiceChatInput, error) {
	input := voiceChatInput{SampleRate: r.Header.Get("X-Sample-Rate"), Controls: controls}
	mr := multipart.NewReader(r.Body, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return input, fmt.Errorf("read multipart request: %w", err)
		}
		name := strings.ToLower(strings.TrimSpace(part.FormName()))
		partType := strings.ToLower(strings.TrimSpace(part.Header.Get("X-Qnap-Part-Type")))
		contentType := strings.TrimSpace(part.Header.Get("Content-Type"))
		mediaType, _, _ := mime.ParseMediaType(contentType)
		partSampleRate := strings.TrimSpace(part.Header.Get("X-Sample-Rate"))

		isContext := name == "context" || partType == "context" || (mediaType == "application/json" && input.Audio == nil)
		isAudio := name == "audio" || partType == "audio" || strings.HasPrefix(mediaType, "audio/") || mediaType == "application/octet-stream"

		switch {
		case isContext:
			b, err := readVoiceBounded(part, voiceMaxContextBytes, "voice context")
			part.Close()
			if err != nil {
				return input, err
			}
			var c voiceChatControls
			if err := json.Unmarshal(b, &c); err != nil {
				return input, fmt.Errorf("decode multipart voice context: %w", err)
			}
			mergeVoiceControls(&input.Controls, c)

		case isAudio:
			if input.Audio != nil {
				part.Close()
				return input, fmt.Errorf("multiple audio parts are not supported")
			}
			b, err := readVoiceBounded(part, voiceMaxAudioBytes, "audio")
			part.Close()
			if err != nil {
				return input, err
			}
			input.Audio = b
			input.AudioContentType = contentType
			if partSampleRate != "" {
				input.SampleRate = partSampleRate
			}

		default:
			_, _ = io.Copy(io.Discard, part)
			part.Close()
		}
	}
	if len(input.Audio) == 0 {
		return input, fmt.Errorf("audio part required")
	}
	if input.AudioContentType == "" {
		input.AudioContentType = "application/octet-stream"
	}
	if err := normalizeVoiceControls(&input.Controls); err != nil {
		return input, err
	}
	return input, nil
}

func parseVoiceChatInput(r *http.Request) (voiceChatInput, error) {
	controls, err := queryVoiceControls(r)
	if err != nil {
		return voiceChatInput{}, err
	}
	headerControls, err := decodeVoiceContextHeader(r.Header.Get("X-Qnap-Voice-Context"))
	if err != nil {
		return voiceChatInput{}, err
	}
	mergeVoiceControls(&controls, headerControls)

	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr == nil && (mediaType == "multipart/form-data" || mediaType == "multipart/mixed") {
		boundary := params["boundary"]
		if boundary == "" {
			return voiceChatInput{}, fmt.Errorf("multipart boundary required")
		}
		return parseVoiceMultipart(r, boundary, controls)
	}

	audio, err := readVoiceBounded(r.Body, voiceMaxAudioBytes, "audio")
	if err != nil {
		return voiceChatInput{}, err
	}
	if len(audio) == 0 {
		return voiceChatInput{}, fmt.Errorf("audio body required")
	}
	if err := normalizeVoiceControls(&controls); err != nil {
		return voiceChatInput{}, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return voiceChatInput{
		Audio:            audio,
		AudioContentType: contentType,
		SampleRate:       r.Header.Get("X-Sample-Rate"),
		Controls:         controls,
	}, nil
}

func voiceLLMMessages(cfg config, transcript string, controls voiceChatControls) []map[string]string {
	defaultSystem := get(cfg, "VOICE_SYSTEM_PROMPT", "あなたは音声アシスタントです。日本語で簡潔に答えてください。原則1文、必要な場合でも最大2文。")
	system := defaultSystem
	if controls.System != nil {
		system = *controls.System
	}
	messages := make([]map[string]string, 0, len(controls.History)+2)
	if strings.TrimSpace(system) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": system})
	}
	for _, msg := range controls.History {
		messages = append(messages, map[string]string{"role": msg.Role, "content": msg.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": strings.TrimSpace(transcript)})
	return messages
}

func voiceEffectiveMaxTokens(cfg config, controls voiceChatControls) int {
	if controls.MaxTokens != nil {
		return *controls.MaxTokens
	}
	return voiceReplyMaxTokens(cfg)
}
