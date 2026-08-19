package main

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTakeVoiceChunkHardBoundary(t *testing.T) {
	chunk, rest, ok := takeVoiceChunk("最初の回答です。続きがあります", 12, 28)
	if !ok || chunk != "最初の回答です。" || rest != "続きがあります" {
		t.Fatalf("got ok=%v chunk=%q rest=%q", ok, chunk, rest)
	}
}

func TestTakeVoiceChunkFallsBackAtRuneLimit(t *testing.T) {
	pending := strings.Repeat("あ", 40)
	chunk, rest, ok := takeVoiceChunk(pending, 12, 28)
	if !ok || utf8.RuneCountInString(chunk) != 28 || utf8.RuneCountInString(rest) != 12 {
		t.Fatalf("got ok=%v chunk=%d rest=%d", ok, utf8.RuneCountInString(chunk), utf8.RuneCountInString(rest))
	}
}

func TestM5ProfileDefaults(t *testing.T) {
	p := voiceProfileFromConfig(defaults(config{}), "m5go")
	if p.SampleRate != 16000 || p.StreamFormat != "multipart" || !p.StripEmoji {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if math.Abs(p.PeakTarget-0.12) > 1e-9 {
		t.Fatalf("peak target=%f", p.PeakTarget)
	}
}

func TestStripVoiceEmoji(t *testing.T) {
	got := stripVoiceEmoji("こんにちは😊！今日は☀です")
	if strings.Contains(got, "😊") || strings.Contains(got, "☀") {
		t.Fatalf("emoji remained: %q", got)
	}
}

func TestResample22050To16000(t *testing.T) {
	src := make([]float32, 22050)
	for i := range src {
		src[i] = float32(math.Sin(2 * math.Pi * 1000 * float64(i) / 22050))
	}
	out := resampleBandlimited(src, 22050, 16000)
	if len(out) < 15999 || len(out) > 16001 {
		t.Fatalf("len=%d", len(out))
	}
	if audioPeak(out) < 0.8 {
		t.Fatalf("1 kHz tone lost too much amplitude: %f", audioPeak(out))
	}
}

func TestPeakLimiterOnlyAttenuates(t *testing.T) {
	s := []float32{0.9, -0.5, 0.1}
	peak := limitAudioPeak(s, 0.12)
	if math.Abs(peak-0.12) > 0.002 {
		t.Fatalf("peak=%f", peak)
	}
	quiet := []float32{0.05, -0.04}
	peak2 := limitAudioPeak(quiet, 0.12)
	if math.Abs(peak2-0.05) > 0.002 {
		t.Fatalf("quiet audio was amplified: %f", peak2)
	}
}
