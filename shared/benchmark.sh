#!/bin/sh
set -eu
QPKG_DIR="${1:-$(cd "$(dirname "$0")" && pwd)}"
CONFIG_FILE="/share/Public/QnapAssistant/config.env"
[ -f "$CONFIG_FILE" ] && . "$CONFIG_FILE"
: "${MODEL_PATH:=/share/Public/Qwen3-0.6B-Q8_0.gguf}"
: "${THREADS:=4}"
OUT="/share/Public/QnapAssistant/benchmark.txt"
mkdir -p "$(dirname "$OUT")"
"$QPKG_DIR/bin/llama-bench" -m "$MODEL_PATH" -t "$THREADS" -p 128 -n 64 | tee "$OUT"
