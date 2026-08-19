#!/bin/sh
set -eu

CONFIG_FILE="${1:-/share/Public/QnapAssistant/config.env}"
[ -f "$CONFIG_FILE" ] && . "$CONFIG_FILE"

: "${MODEL_PATH:=/share/Public/Qwen3-0.6B-Q8_0.gguf}"
: "${MODEL_URL:=https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf?download=true}"
: "${MIN_MODEL_BYTES:=100000000}"

size_of() {
    wc -c < "$1" | tr -d ' '
}

if [ -s "$MODEL_PATH" ] && [ "$(size_of "$MODEL_PATH")" -ge "$MIN_MODEL_BYTES" ]; then
    echo "Model already exists: $MODEL_PATH"
    exit 0
fi

MODEL_DIR=$(dirname "$MODEL_PATH")
mkdir -p "$MODEL_DIR"
TMP_PATH="${MODEL_PATH}.part"

echo "Downloading Qwen3-0.6B to $MODEL_PATH"
if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 5 --retry-delay 5 --connect-timeout 30 -C - -o "$TMP_PATH" "$MODEL_URL"
elif command -v wget >/dev/null 2>&1; then
    wget -c --tries=5 --timeout=30 -O "$TMP_PATH" "$MODEL_URL"
else
    echo "Neither curl nor wget is available; cannot download Qwen3-0.6B." >&2
    exit 1
fi

SIZE=$(size_of "$TMP_PATH")
if [ "$SIZE" -lt "$MIN_MODEL_BYTES" ]; then
    echo "Downloaded file is unexpectedly small ($SIZE bytes); keeping partial file for inspection." >&2
    exit 1
fi

mv "$TMP_PATH" "$MODEL_PATH"
printf 'Model downloaded to %s (%s bytes)\n' "$MODEL_PATH" "$SIZE"
