package main

import (
	"strings"
	"unicode"
)

func hardVoiceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '\n':
		return true
	default:
		return false
	}
}

func softVoiceBoundary(r rune) bool {
	switch r {
	case '、', '，', ',', '：', ':', '；', ';':
		return true
	default:
		return unicode.IsSpace(r)
	}
}

func takeVoiceChunk(pending string, minChars, maxChars int) (chunk, rest string, ok bool) {
	pending = strings.TrimLeftFunc(pending, unicode.IsSpace)
	if pending == "" {
		return "", "", false
	}
	runes := []rune(pending)
	for i, r := range runes {
		if !hardVoiceBoundary(r) {
			continue
		}
		chunk = strings.TrimSpace(string(runes[:i+1]))
		rest = string(runes[i+1:])
		if chunk == "" {
			return takeVoiceChunk(rest, minChars, maxChars)
		}
		return chunk, rest, true
	}
	if len(runes) < maxChars {
		return "", pending, false
	}
	cut := maxChars
	for i := maxChars - 1; i >= minChars-1; i-- {
		if softVoiceBoundary(runes[i]) {
			cut = i + 1
			break
		}
	}
	chunk = strings.TrimSpace(string(runes[:cut]))
	rest = string(runes[cut:])
	if chunk == "" {
		return "", pending, false
	}
	return chunk, rest, true
}
