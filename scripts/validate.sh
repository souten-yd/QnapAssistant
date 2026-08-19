#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for f in "$ROOT/shared/start-stop.sh" "$ROOT/shared/download-model.sh" "$ROOT/shared/download-voice-models.sh" "$ROOT/shared/benchmark.sh" "$ROOT/shared/voice-benchmark.sh"; do
  sh -n "$f"
done
for f in "$ROOT/scripts/build-llama.sh" "$ROOT/scripts/build-voice-worker.sh" "$ROOT/scripts/build-qpkg.sh" "$ROOT/scripts/bootstrap-llama-from-qpkg.sh"; do
  bash -n "$f"
done
grep -q '^MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf$' "$ROOT/shared/config.env.default"
grep -q '^MODEL_DIR=/share/Public$' "$ROOT/shared/config.env.default"
grep -q '^THINKING_MODE=off$' "$ROOT/shared/config.env.default"
grep -q '^KEEP_MODELS_LOADED=1$' "$ROOT/shared/config.env.default"
grep -q '^IDLE_TIMEOUT_SECONDS=0$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_PORT=11437$' "$ROOT/shared/config.env.default"
grep -q '^ASR_LANGUAGE=ja$' "$ROOT/shared/config.env.default"
grep -q '^TTS_LANGUAGE=ja$' "$ROOT/shared/config.env.default"
grep -q '^TTS_STEPS=4$' "$ROOT/shared/config.env.default"
grep -q '^QPKG_VER="0.3.3"$' "$ROOT/qpkg.cfg"
grep -q 'QPKG_SERVICE_PORT="11435"' "$ROOT/qpkg.cfg"
grep -q 'QPKG_SERVICE_PIDFILE="/share/Public/QnapAssistant/qnapassistant.pid"' "$ROOT/qpkg.cfg"
grep -q 'find_running_pid' "$ROOT/shared/start-stop.sh"
grep -q '/api/voice/models/download' "$ROOT/admin/main.go"
grep -q '/api/voice/piper/download' "$ROOT/admin/main.go"
grep -q '/api/bootstrap' "$ROOT/admin/main.go"
grep -q 'm.autoProvision()' "$ROOT/admin/main.go"
grep -q 'resident LLM ready' "$ROOT/admin/main.go"
grep -q 'openJTalkArchiveSHA256' "$ROOT/admin/bootstrap.go"
grep -q 'KEEP_MODELS_LOADED' "$ROOT/admin/manager.go"
grep -q 'OPENJTALK_DICTIONARY_PATH' "$ROOT/voiceworker/piper.go"
grep -q 'piper_resident' "$ROOT/voiceworker/main.go"
grep -q -- '--json-input' "$ROOT/voiceworker/piper_resident.go"
grep -q 'requestPiperResidentLocked' "$ROOT/voiceworker/piper_resident.go"
grep -q 'e.preload()' "$ROOT/voiceworker/main.go"
grep -q 'getenv("TTS_BACKEND", "piper_plus")' "$ROOT/voiceworker/main.go"
grep -q 'piperRuntimeSHA256' "$ROOT/admin/piper_models.go"
grep -q 'backend,omitempty' "$ROOT/voiceworker/engine.go"
grep -q 'X-Qnap-TTS-Backend' "$ROOT/voiceworker/main.go"
grep -q 'Unload Voice' "$ROOT/admin/ui.go"
if grep -Eq '^[[:space:]]*kill[[:space:]]+-0([[:space:]]|$)' "$ROOT/shared/start-stop.sh"; then
  echo "start-stop.sh must not execute kill -0 for cross-user status detection" >&2
  exit 1
fi
[ -x "$ROOT/x86_64/bin/qnap-voice-worker" ] || { echo "voice worker binary missing" >&2; exit 1; }
[ -f "$ROOT/icons/QnapAssistant.png" ] || { echo "QPKG icon missing" >&2; exit 1; }
[ -f "$ROOT/icons/QnapAssistant_80.png" ] || { echo "QPKG 80px icon missing" >&2; exit 1; }
(cd "$ROOT/admin" && go test ./... && go vet ./...)
echo "Static validation passed."
