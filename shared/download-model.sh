#!/bin/sh
set -eu

CONFIG_FILE="${1:-/share/Public/QnapAssistant/config.env}"
[ -f "$CONFIG_FILE" ] && . "$CONFIG_FILE"

: "${MODEL_PATH:=/share/Public/Qwen3-0.6B-Q8_0.gguf}"
: "${MODEL_URL:=https://huggingface.co/Qwen/Qwen3-0.6B-GGUF/resolve/1eaf4d9657fe65ad10a51eab76a8db5b363bddaa/Qwen3-0.6B-Q8_0.gguf?download=true}"
: "${MODEL_SHA256:=9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031}"
: "${MIN_MODEL_BYTES:=100000000}"

size_of() {
    wc -c < "$1" | tr -d ' '
}

verify_model() {
    FILE="$1"
    [ -s "$FILE" ] || return 1
    SIZE=$(size_of "$FILE")
    [ "$SIZE" -ge "$MIN_MODEL_BYTES" ] || return 1
    if [ -n "$MODEL_SHA256" ] && command -v sha256sum >/dev/null 2>&1; then
        ACTUAL_SHA256=$(sha256sum "$FILE" | awk '{print $1}')
        if [ "$ACTUAL_SHA256" != "$MODEL_SHA256" ]; then
            echo "SHA-256 mismatch for $FILE" >&2
            echo "expected: $MODEL_SHA256" >&2
            echo "actual:   $ACTUAL_SHA256" >&2
            return 1
        fi
    fi
    return 0
}

if verify_model "$MODEL_PATH"; then
    echo "Model already exists and passed validation: $MODEL_PATH"
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

if ! verify_model "$TMP_PATH"; then
    SIZE=$(size_of "$TMP_PATH" 2>/dev/null || echo 0)
    echo "Downloaded model failed validation ($SIZE bytes); keeping $TMP_PATH for inspection." >&2
    exit 1
fi

SIZE=$(size_of "$TMP_PATH")
mv "$TMP_PATH" "$MODEL_PATH"
printf 'Model downloaded and verified at %s (%s bytes)\n' "$MODEL_PATH" "$SIZE"
