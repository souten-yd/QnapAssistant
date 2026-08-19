#!/bin/sh
set -eu
BASE="${BASE_URL:-http://127.0.0.1:11435}"
VOICE_DIR="${VOICE_DIR:-/share/Public/QnapAssistant/voice}"
ASR_DIR="${ASR_MODEL_DIR:-$VOICE_DIR/sensevoice}"
OUT="${VOICE_BENCHMARK_OUT:-/share/Public/QnapAssistant/voice-benchmark.txt}"
WAV="$ASR_DIR/test_wavs/ja.wav"
[ -f "$WAV" ] || WAV="$ASR_DIR/test_wavs/en.wav"
[ -f "$WAV" ] || { echo "No SenseVoice test WAV found" >&2; exit 1; }

if ! (: > "$OUT") 2>/dev/null; then
  OUT="/tmp/qnapassistant-voice-benchmark.txt"
fi
TMP_PREFIX="/tmp/qnapassistant-voice-benchmark-$$"
trap 'rm -f "${TMP_PREFIX}".*' EXIT INT TERM

tts_test() {
  label="$1"
  json="$2"
  hdr="${TMP_PREFIX}.${label}.headers"
  curl -fsS -D "$hdr" -o /dev/null \
    -H 'Content-Type: application/json' \
    -d "$json" \
    "$BASE/v1/audio/speech"
  grep -i '^X-Qnap-' "$hdr" || true
  rm -f "$hdr"
}

{
  echo "QnapAssistant Voice Benchmark $(date)"
  echo "== STATUS =="
  curl -fsS "$BASE/api/voice/status" || true
  echo

  echo "== ASR warm passes =="
  for pass in 1 2; do
    echo "pass=$pass"
    curl -fsS -H 'Content-Type: audio/wav' --data-binary "@$WAV" "$BASE/v1/audio/transcriptions"
    echo
  done

  echo "== Supertonic baseline steps=4 =="
  tts_test "supertonic" '{"text":"こんにちは。QNAP音声アシスタントの速度を測定しています。","lang":"ja","backend":"supertonic","steps":4,"speed":1.0}'

  echo "== Piper Plus candidate cold/warm page-cache passes =="
  for pass in 1 2; do
    echo "pass=$pass"
    tts_test "piper-$pass" '{"text":"こんにちは。QNAP音声アシスタントの速度を測定しています。","lang":"ja","backend":"piper_plus","speed":1.0}'
  done

  echo "== STATUS AFTER =="
  curl -fsS "$BASE/api/voice/status" || true
  echo
  echo "benchmark_output=$OUT"
} | tee "$OUT"
