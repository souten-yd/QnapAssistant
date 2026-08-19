package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

type voiceStreamEvent struct {
	Type       string         `json:"type"`
	Index      int            `json:"index,omitempty"`
	Text       string         `json:"text,omitempty"`
	Transcript string         `json:"transcript,omitempty"`
	Reply      string         `json:"reply,omitempty"`
	Audio      string         `json:"audio_base64,omitempty"`
	AudioType  string         `json:"audio_type,omitempty"`
	SampleRate int            `json:"sample_rate,omitempty"`
	Backend    string         `json:"backend,omitempty"`
	Peak       float64        `json:"peak,omitempty"`
	Error      string         `json:"error,omitempty"`
	Timings    map[string]any `json:"timings,omitempty"`
}

type voiceStreamWriter interface {
	WriteEvent(voiceStreamEvent) error
	WriteAudio(index int, text string, audio processedVoiceAudio, backend string, timings map[string]any) error
	Close() error
}

type ndjsonVoiceWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newNDJSONVoiceWriter(w http.ResponseWriter, flusher http.Flusher, p voiceClientProfile) voiceStreamWriter {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Qnap-Voice-Profile", p.Name)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &ndjsonVoiceWriter{w: w, flusher: flusher}
}

func (n *ndjsonVoiceWriter) WriteEvent(event voiceStreamEvent) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := n.w.Write(b); err != nil {
		return err
	}
	n.flusher.Flush()
	return nil
}

func (n *ndjsonVoiceWriter) WriteAudio(index int, text string, audio processedVoiceAudio, backend string, timings map[string]any) error {
	return n.WriteEvent(voiceStreamEvent{Type: "audio", Index: index, Text: text, Audio: base64.StdEncoding.EncodeToString(audio.WAV), AudioType: "audio/wav", SampleRate: audio.SampleRate, Backend: backend, Peak: audio.Peak, Timings: timings})
}

func (n *ndjsonVoiceWriter) Close() error { return nil }

const voiceMultipartBoundary = "qnapassistant-v04"

type multipartVoiceWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	closed  bool
}

func newMultipartVoiceWriter(w http.ResponseWriter, flusher http.Flusher, p voiceClientProfile) voiceStreamWriter {
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+voiceMultipartBoundary)
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Qnap-Voice-Profile", p.Name)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &multipartVoiceWriter{w: w, flusher: flusher}
}

func (m *multipartVoiceWriter) writePart(contentType string, headers map[string]string, body []byte) error {
	if m.closed {
		return fmt.Errorf("multipart voice stream is closed")
	}
	if _, err := fmt.Fprintf(m.w, "--%s\r\nContent-Type: %s\r\nContent-Length: %d\r\n", voiceMultipartBoundary, contentType, len(body)); err != nil {
		return err
	}
	for k, v := range headers {
		if _, err := fmt.Fprintf(m.w, "%s: %s\r\n", k, v); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(m.w, "\r\n"); err != nil {
		return err
	}
	if _, err := m.w.Write(body); err != nil {
		return err
	}
	if _, err := fmt.Fprint(m.w, "\r\n"); err != nil {
		return err
	}
	m.flusher.Flush()
	return nil
}

func (m *multipartVoiceWriter) WriteEvent(event voiceStreamEvent) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return m.writePart("application/json; charset=utf-8", map[string]string{"X-Qnap-Part-Type": event.Type}, b)
}

func (m *multipartVoiceWriter) WriteAudio(index int, text string, audio processedVoiceAudio, backend string, timings map[string]any) error {
	headers := map[string]string{"X-Qnap-Part-Type": "audio", "X-Qnap-Index": strconv.Itoa(index), "X-Qnap-Sample-Rate": strconv.Itoa(audio.SampleRate), "X-Qnap-Source-Rate": strconv.Itoa(audio.SourceSampleRate), "X-Qnap-Peak": fmt.Sprintf("%.6f", audio.Peak), "X-Qnap-TTS-Backend": backend}
	if v, ok := timings["tts_engine_ms"]; ok {
		headers["X-Qnap-Processing-Ms"] = fmt.Sprint(v)
	}
	if v, ok := timings["tts_rtf"]; ok {
		headers["X-Qnap-RTF"] = fmt.Sprint(v)
	}
	return m.writePart("audio/wav", headers, audio.WAV)
}

func (m *multipartVoiceWriter) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	_, err := fmt.Fprintf(m.w, "--%s--\r\n", voiceMultipartBoundary)
	m.flusher.Flush()
	return err
}

func newVoiceStreamWriter(w http.ResponseWriter, flusher http.Flusher, p voiceClientProfile) voiceStreamWriter {
	if p.StreamFormat == "multipart" {
		return newMultipartVoiceWriter(w, flusher, p)
	}
	return newNDJSONVoiceWriter(w, flusher, p)
}
