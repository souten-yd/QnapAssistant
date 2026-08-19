#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for f in "$ROOT/shared/start-stop.sh" "$ROOT/shared/launch.sh" "$ROOT/shared/download-model.sh" "$ROOT/shared/benchmark.sh"; do
  sh -n "$f"
done
bash -n "$ROOT/scripts/build-llama.sh"
bash -n "$ROOT/scripts/build-qpkg.sh"
grep -q '^MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf$' "$ROOT/shared/config.env.default"
grep -q 'Qwen/Qwen3-0.6B-GGUF/resolve/main/Qwen3-0.6B-Q8_0.gguf' "$ROOT/shared/config.env.default"
grep -q 'QPKG_SERVICE_PORT="11435"' "$ROOT/qpkg.cfg"
echo "Static validation passed."
