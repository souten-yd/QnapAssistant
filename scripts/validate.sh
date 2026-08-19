#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for f in "$ROOT/shared/start-stop.sh" "$ROOT/shared/download-model.sh" "$ROOT/shared/benchmark.sh"; do
  sh -n "$f"
done
bash -n "$ROOT/scripts/build-llama.sh"
bash -n "$ROOT/scripts/build-qpkg.sh"
bash -n "$ROOT/scripts/bootstrap-llama-from-qpkg.sh"
grep -q '^MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf$' "$ROOT/shared/config.env.default"
grep -q '^MODEL_DIR=/share/Public$' "$ROOT/shared/config.env.default"
grep -q '^THINKING_MODE=off$' "$ROOT/shared/config.env.default"
grep -q '^IDLE_TIMEOUT_SECONDS=300$' "$ROOT/shared/config.env.default"
grep -q 'QPKG_SERVICE_PORT="11435"' "$ROOT/qpkg.cfg"
grep -q 'QPKG_SERVICE_PIDFILE="/share/Public/QnapAssistant/qnapassistant.pid"' "$ROOT/qpkg.cfg"
grep -q 'find_running_pid' "$ROOT/shared/start-stop.sh"
(cd "$ROOT/admin" && go test ./... && go vet ./...)
echo "Static validation passed."
