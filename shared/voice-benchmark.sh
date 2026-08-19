#!/bin/sh
set -eu
BASE="${BASE_URL:-http://127.0.0.1:11435}"
VOICE_DIR="${VOICE_DIR:-/share/Public/QnapAssistant/voice}"
ASR_DIR="${ASR_MODEL_DIR:-$VOICE_DIR/sensevoice}"
OUT="/share/Public/QnapAssistant/voice-benchmark.txt"
WAV="$ASR_DIR/test_wavs/ja.wav"
[ -f "$WAV" ] || WAV="$ASR_DIR/test_wavs/en.wav"
[ -f "$WAV" ] || { echo "No SenseVoice test WAV found" >&2; exit 1; }

{
  echo "QnapAssistant Voice Benchmark $(date)"
  echo "== STATUS =="
  curl -fsS "$BASE/api/voice/status" || true
  echo
  echo "== ASR =="
  curl -fsS -H 'Content-Type: audio/wav' --data-binary "@$WAV" "$BASE/v1/audio/transcriptions"
  echo
  echo "== TTS steps 4/6/8 =="
  for steps in 4 6 8; do
    hdr="$VOICE_DIR/tts-$steps.headers"
    wav="$VOICE_DIR/tts-$steps.wav"
    curl -fsS -D "$hdr" -o "$wav" -H 'Content-Type: application/json' \
      -d "{\"text\":\"こんにちは。QNAP音声アシスタントの速度を測定しています。\",\"lang\":\"ja\",\"steps\":$steps,\"speed\":1.0}" \
      "$BASE/v1/audio/speech"
    echo "steps=$steps"
    grep -i '^X-Qnap-' "$hdr" || true
  done
} | tee "$OUT"
