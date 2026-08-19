package main

import (
	"net/http"
	"strconv"
	"strings"
)

type voiceClientProfile struct {
	Name          string
	SampleRate    int
	PeakTarget    float64
	StripEmoji    bool
	StreamFormat  string
	ChunkMinChars int
	ChunkMaxChars int
}

func boolConfig(c config, key string, d bool) bool {
	v := strings.ToLower(strings.TrimSpace(get(c, key, "")))
	if v == "" {
		return d
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return d
	}
}

func normalizedVoiceProfileName(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "m5", "m5go", "m5-go", "m5stack", "m5-stack":
		return "m5go"
	case "generic", "default", "normal", "desktop", "web":
		return "generic"
	default:
		return ""
	}
}

func voiceProfileFromConfig(c config, name string) voiceClientProfile {
	name = normalizedVoiceProfileName(name)
	if name == "" {
		name = normalizedVoiceProfileName(get(c, "VOICE_PROFILE_DEFAULT", "generic"))
		if name == "" {
			name = "generic"
		}
	}
	prefix := "VOICE_GENERIC_"
	defaults := voiceClientProfile{Name: "generic", SampleRate: 0, PeakTarget: 0, StripEmoji: false, StreamFormat: "ndjson", ChunkMinChars: 12, ChunkMaxChars: 28}
	if name == "m5go" {
		prefix = "VOICE_M5_"
		defaults = voiceClientProfile{Name: "m5go", SampleRate: 16000, PeakTarget: 0.12, StripEmoji: true, StreamFormat: "multipart", ChunkMinChars: 8, ChunkMaxChars: 18}
	}
	defaults.SampleRate = intVal(c, prefix+"SAMPLE_RATE", defaults.SampleRate)
	defaults.PeakTarget = floatVal(c, prefix+"PEAK_TARGET", defaults.PeakTarget)
	if defaults.PeakTarget < 0 {
		defaults.PeakTarget = 0
	}
	if defaults.PeakTarget > 1 {
		defaults.PeakTarget = 1
	}
	defaults.StripEmoji = boolConfig(c, prefix+"STRIP_EMOJI", defaults.StripEmoji)
	format := strings.ToLower(strings.TrimSpace(get(c, prefix+"STREAM_FORMAT", defaults.StreamFormat)))
	if format != "multipart" && format != "ndjson" {
		format = defaults.StreamFormat
	}
	defaults.StreamFormat = format
	defaults.ChunkMinChars = intVal(c, prefix+"CHUNK_MIN_CHARS", defaults.ChunkMinChars)
	defaults.ChunkMaxChars = intVal(c, prefix+"CHUNK_MAX_CHARS", defaults.ChunkMaxChars)
	if defaults.ChunkMinChars < 4 {
		defaults.ChunkMinChars = 4
	}
	if defaults.ChunkMaxChars < defaults.ChunkMinChars+4 {
		defaults.ChunkMaxChars = defaults.ChunkMinChars + 4
	}
	if defaults.ChunkMaxChars > 128 {
		defaults.ChunkMaxChars = 128
	}
	return defaults
}

func requestedVoiceProfile(r *http.Request, c config, bodyProfile string) voiceClientProfile {
	name := normalizedVoiceProfileName(bodyProfile)
	if name == "" {
		name = normalizedVoiceProfileName(r.URL.Query().Get("profile"))
	}
	if name == "" {
		name = normalizedVoiceProfileName(r.Header.Get("X-Qnap-Voice-Profile"))
	}
	if name == "" {
		name = normalizedVoiceProfileName(get(c, "VOICE_PROFILE_DEFAULT", "generic"))
	}
	return voiceProfileFromConfig(c, name)
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return true
	case r == 0xFE0F || r == 0x200D:
		return true
	default:
		return false
	}
}

func stripVoiceEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmojiRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func profileText(p voiceClientProfile, s string) string {
	if p.StripEmoji {
		return stripVoiceEmoji(s)
	}
	return strings.TrimSpace(s)
}

func intPointerValue(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

func floatPointerValue(v *float64, d float64) float64 {
	if v == nil {
		return d
	}
	return *v
}

func profileResponseHeaders(w http.ResponseWriter, p voiceClientProfile) {
	w.Header().Set("X-Qnap-Voice-Profile", p.Name)
	if p.SampleRate > 0 {
		w.Header().Set("X-Qnap-Requested-Sample-Rate", strconv.Itoa(p.SampleRate))
	}
}
