#!/bin/sh
set -eu

QPKG_DIR="${1:?QPKG directory is required}"
CONFIG_FILE="/share/Public/QnapAssistant/config.env"
[ -f "$CONFIG_FILE" ] && . "$CONFIG_FILE"

: "${MODEL_PATH:=/share/Public/Qwen3-0.6B-Q8_0.gguf}"
: "${HOST:=0.0.0.0}"
: "${PORT:=11435}"
: "${THREADS:=4}"
: "${THREADS_BATCH:=4}"
: "${CONTEXT:=4096}"
: "${BATCH:=256}"
: "${UBATCH:=128}"
: "${PARALLEL:=1}"
: "${EXTRA_ARGS:=}"

if [ ! -s "$MODEL_PATH" ]; then
    "$QPKG_DIR/download-model.sh" "$CONFIG_FILE" &
    DOWNLOAD_PID=$!
    trap 'kill "$DOWNLOAD_PID" 2>/dev/null || true; exit 0' HUP INT TERM
    wait "$DOWNLOAD_PID"
    trap - HUP INT TERM
fi

SERVER="$QPKG_DIR/bin/llama-server"
if [ ! -x "$SERVER" ]; then
    echo "llama-server is missing or not executable: $SERVER" >&2
    exit 1
fi

# EXTRA_ARGS is intentionally shell-expanded to permit advanced llama-server options.
# shellcheck disable=SC2086
exec "$SERVER" \
    --model "$MODEL_PATH" \
    --host "$HOST" \
    --port "$PORT" \
    --threads "$THREADS" \
    --threads-batch "$THREADS_BATCH" \
    --ctx-size "$CONTEXT" \
    --batch-size "$BATCH" \
    --ubatch-size "$UBATCH" \
    --parallel "$PARALLEL" \
    $EXTRA_ARGS
