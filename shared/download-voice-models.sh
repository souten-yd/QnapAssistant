#!/bin/sh
set -eu

VOICE_DIR="${VOICE_DIR:-/share/Public/QnapAssistant/voice}"
ASR_DIR="${ASR_MODEL_DIR:-$VOICE_DIR/sensevoice}"
TTS_DIR="${TTS_MODEL_DIR:-$VOICE_DIR/supertonic3}"
TMP="$VOICE_DIR/.downloads"
ASR_NAME="sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17"
TTS_NAME="sherpa-onnx-supertonic-3-tts-int8-2026-05-11"
ASR_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/$ASR_NAME.tar.bz2"
TTS_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/$TTS_NAME.tar.bz2"

mkdir -p "$VOICE_DIR" "$TMP"

fetch() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 -C - -o "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -c -O "$out" "$url"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

extract_model() {
  url="$1"
  name="$2"
  dest="$3"
  marker="$4"
  if [ -s "$dest/$marker" ]; then
    echo "$name already installed at $dest"
    return
  fi
  archive="$TMP/$name.tar.bz2"
  work="$TMP/$name.extract"
  rm -rf "$work"
  mkdir -p "$work"
  fetch "$url" "$archive"
  if ! tar -xjf "$archive" -C "$work"; then
    echo "QTS tar/bzip2 support is required to extract $archive" >&2
    exit 1
  fi
  src="$work/$name"
  [ -d "$src" ] || { echo "Unexpected archive layout for $name" >&2; exit 1; }
  rm -rf "$dest.new"
  mv "$src" "$dest.new"
  rm -rf "$dest"
  mv "$dest.new" "$dest"
  rm -rf "$work" "$archive"
}

extract_model "$ASR_URL" "$ASR_NAME" "$ASR_DIR" "model.int8.onnx"
extract_model "$TTS_URL" "$TTS_NAME" "$TTS_DIR" "duration_predictor.int8.onnx"

[ -s "$ASR_DIR/model.int8.onnx" ] && [ -s "$ASR_DIR/tokens.txt" ]
for f in duration_predictor.int8.onnx text_encoder.int8.onnx vector_estimator.int8.onnx vocoder.int8.onnx tts.json unicode_indexer.bin voice.bin; do
  [ -s "$TTS_DIR/$f" ] || { echo "Missing TTS file: $f" >&2; exit 1; }
done

echo "Voice models ready."
echo "ASR: $ASR_DIR"
echo "TTS: $TTS_DIR"
