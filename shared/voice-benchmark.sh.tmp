#!/bin/sh
set -eu
BASE="${BASE_URL:-http://127.0.0.1:11435}"
VOICE_DIR="${VOICE_DIR:-/share/Public/QnapAssistant/voice}"
ASR_DIR="${ASR_MODEL_DIR:-$VOICE_DIR/sensevoice}"
OUT="/share/Public/QnapAssistant/voice-benchmark.txt"
WAV="$ASR_DIR/test_wavs/ja.wav"
[ -f "$WAV" ] || WAV="$ASR_DIR/test_wavs/en.wav"
[ -f "$WAV" ] || { echo "No SenseVoice test WAV found" >&2; exit 1; }
TMP_PREFIX="/tmp/qnapassistant-voice-benchmark-$$"
trap 'rm -f "${TMP_PREFIX}".*' EXIT INT TERM

{
  echo "QnapAssistant Voice Benchmark $(date)"
  echo "== STATUS =="
  curl -fsS "$BASE/api/voice/status" || true
  echo
  echo "== ASR cold/warm =="
  for pass in 1 2; do
    echo "pass=$pass"
    curl -fsS -H 'Content-Type: audio/wav' --data-binary "@$WAV" "$BASE/v1/audio/transcriptions"
    echo
  done
  echo "== TTS steps 4/6/8 =="
  for steps in 4 6 8; do
    hdr="${TMP_PREFIX}.tts-$steps.headers"
    curl -fsS -D "$hdr" -o /dev/null -H 'Content-Type: application/json' \
      -d "{\"text\":\"こんにちは。QNAP音声アシスタントの速度を測定しています。\",\"lang\":\"ja\",\"steps\":$steps,\"speed\":1.0}" \
      "$BASE/v1/audio/speech"
    echo "steps=$steps"
    grep -i '^X-Qnap-' "$hdr" || true
    rm -f "$hdr"
  done
  echo "== STATUS AFTER =="
  curl -fsS "$BASE/api/voice/status" || true
  echo
} | tee "$OUT"
