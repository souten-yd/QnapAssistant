#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for f in "$ROOT/shared/start-stop.sh" "$ROOT/shared/download-model.sh" "$ROOT/shared/download-voice-models.sh" "$ROOT/shared/benchmark.sh" "$ROOT/shared/voice-benchmark.sh"; do
  sh -n "$f"
done
for f in "$ROOT/scripts/build-llama.sh" "$ROOT/scripts/build-voice-worker.sh" "$ROOT/scripts/build-qpkg.sh" "$ROOT/scripts/bootstrap-llama-from-qpkg.sh" "$ROOT/scripts/stage-piper-qts-compat.sh"; do
  bash -n "$f"
done
grep -q '^MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf$' "$ROOT/shared/config.env.default"
grep -q '^MODEL_DIR=/share/Public$' "$ROOT/shared/config.env.default"
grep -q '^THINKING_MODE=off$' "$ROOT/shared/config.env.default"
grep -q '^IDLE_TIMEOUT_SECONDS=300$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_PORT=11437$' "$ROOT/shared/config.env.default"
grep -q '^ASR_LANGUAGE=ja$' "$ROOT/shared/config.env.default"
grep -q '^TTS_LANGUAGE=ja$' "$ROOT/shared/config.env.default"
grep -q '^QPKG_VER="0.3.1"$' "$ROOT/qpkg.cfg"
grep -q 'QPKG_SERVICE_PORT="11435"' "$ROOT/qpkg.cfg"
grep -q 'QPKG_SERVICE_PIDFILE="/share/Public/QnapAssistant/qnapassistant.pid"' "$ROOT/qpkg.cfg"
grep -q 'find_running_pid' "$ROOT/shared/start-stop.sh"
grep -q '/api/voice/models/download' "$ROOT/admin/main.go"
grep -q '/api/voice/piper/download' "$ROOT/admin/main.go"
grep -q 'piperRuntimeSHA256' "$ROOT/admin/piper_models.go"
grep -q 'backend,omitempty' "$ROOT/voiceworker/engine.go"
grep -q 'X-Qnap-TTS-Backend' "$ROOT/voiceworker/main.go"
grep -q 'qts-compat-loader' "$ROOT/voiceworker/piper.go"
if grep -Eq '^[[:space:]]*kill[[:space:]]+-0([[:space:]]|$)' "$ROOT/shared/start-stop.sh"; then
  echo "start-stop.sh must not execute kill -0 for cross-user status detection" >&2
  exit 1
fi
[ -x "$ROOT/x86_64/bin/qnap-voice-worker" ] || { echo "voice worker binary missing" >&2; exit 1; }
[ -x "$ROOT/x86_64/piper-compat/ld-linux-x86-64.so.2" ] || { echo "Piper QTS compatibility loader missing" >&2; exit 1; }
[ -s "$ROOT/x86_64/piper-compat/lib/libc.so.6" ] || { echo "Piper compatibility libc missing" >&2; exit 1; }
[ -s "$ROOT/x86_64/piper-compat/lib/libstdc++.so.6" ] || { echo "Piper compatibility libstdc++ missing" >&2; exit 1; }
"$ROOT/x86_64/piper-compat/ld-linux-x86-64.so.2" --library-path "$ROOT/x86_64/piper-compat/lib" /usr/bin/true
(cd "$ROOT/admin" && go test ./... && go vet ./...)
echo "Static validation passed."
