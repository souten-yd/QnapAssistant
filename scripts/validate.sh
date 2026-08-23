#!/usr/bin/env bash
set -euo pipefail
trap 'rc=$?; echo "validate.sh failed at line ${LINENO}: ${BASH_COMMAND}" >&2; exit $rc' ERR
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for f in "$ROOT/shared/start-stop.sh" "$ROOT/shared/download-model.sh" "$ROOT/shared/download-voice-models.sh" "$ROOT/shared/benchmark.sh" "$ROOT/shared/voice-benchmark.sh" "$ROOT/shared/update-self.sh"; do
  sh -n "$f"
done
for f in "$ROOT/scripts/build-llama.sh" "$ROOT/scripts/build-voice-worker.sh" "$ROOT/scripts/build-qpkg.sh" "$ROOT/scripts/bootstrap-llama-from-qpkg.sh"; do
  bash -n "$f"
done
grep -q '^LLM_PROVIDER=local$' "$ROOT/shared/config.env.default"
grep -q '^LLM_AUTO_UNLOAD=0$' "$ROOT/shared/config.env.default"
grep -q '^LLM_IDLE_TIMEOUT_SECONDS=300$' "$ROOT/shared/config.env.default"
grep -q '^ASR_AUTO_UNLOAD=0$' "$ROOT/shared/config.env.default"
grep -q '^TTS_AUTO_UNLOAD=0$' "$ROOT/shared/config.env.default"
grep -q '^OPENROUTER_BASE_URL=https://openrouter.ai/api/v1$' "$ROOT/shared/config.env.default"
grep -q '^OPENROUTER_MODEL=openrouter/free$' "$ROOT/shared/config.env.default"
if grep -Eq 'OPENROUTER_API_KEY[[:space:]]*=' "$ROOT/shared/config.env.default"; then
  echo "OpenRouter API key must never be stored in shared config" >&2
  exit 1
fi
grep -q '^MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf$' "$ROOT/shared/config.env.default"
grep -q '^THINKING_MODE=off$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_REPLY_MAX_TOKENS=0$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_PROFILE_DEFAULT=generic$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_M5_SAMPLE_RATE=16000$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_M5_PEAK_TARGET=0.12$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_M5_STRIP_EMOJI=1$' "$ROOT/shared/config.env.default"
grep -q '^VOICE_M5_STREAM_FORMAT=multipart$' "$ROOT/shared/config.env.default"
# Accept normal SemVer releases instead of pinning validation to one historical version.
grep -Eq '^QPKG_VER="[0-9]+\.[0-9]+\.[0-9]+"$' "$ROOT/qpkg.cfg"
grep -q '/api/openrouter/key' "$ROOT/admin/main.go"
grep -q '/api/openrouter/models' "$ROOT/admin/main.go"
grep -q '/api/openrouter/test' "$ROOT/admin/main.go"
grep -q '/api/update/check' "$ROOT/admin/main.go"
grep -q '/api/update/status' "$ROOT/admin/main.go"
grep -q '/api/update/apply' "$ROOT/admin/main.go"
grep -q 'X-Qnap-Update-Confirm' "$ROOT/admin/ui_update.go"
grep -q 'sha256' "$ROOT/shared/update-self.sh"
grep -q 'qpkg_cli' "$ROOT/shared/update-self.sh"
grep -q 'openrouter/free' "$ROOT/admin/llm_provider.go"
grep -q '0600' "$ROOT/admin/llm_provider.go"
grep -q 'prepareLLMRequest' "$ROOT/admin/voice_llm_standard.go"
grep -q 'applyOpenRouterRequestConfig' "$ROOT/admin/thinking.go"
grep -q 'llmAutoUnload' "$ROOT/admin/manager.go"
grep -q 'idleUnloadLoop' "$ROOT/voiceworker/main.go"
grep -q 'unloadASR' "$ROOT/voiceworker/idle_unload.go"
grep -q 'unloadTTS' "$ROOT/voiceworker/idle_unload.go"
grep -q '/v1/audio/speech/stream' "$ROOT/admin/main.go"
grep -q 'handleVoiceChatStreamSession' "$ROOT/admin/main.go"
grep -q 'handleVoiceChatSessionAdaptive' "$ROOT/admin/main.go"
grep -q '/api/voice/protocol' "$ROOT/admin/main.go"
grep -q 'X-Qnap-Voice-Context' "$ROOT/admin/main.go"
grep -q 'parseVoiceChatInput' "$ROOT/admin/voice_context.go"
grep -q 'SessionID' "$ROOT/admin/voice_context.go"
grep -q 'voiceLLMMessages' "$ROOT/admin/voice_context.go"
grep -q 'backend_default' "$ROOT/admin/voice_llm_standard.go"
grep -q 'voiceSessionRecentMessages' "$ROOT/admin/voice_session.go"
grep -q 'session_total_messages' "$ROOT/admin/voice_session.go"
grep -q 'handleConfigV04' "$ROOT/admin/main.go"
grep -q 'resampleBandlimited' "$ROOT/admin/voice_audio_v04.go"
grep -q 'X-Qnap-Peak' "$ROOT/admin/voice_speech_v04.go"
grep -q 'multipart/mixed' "$ROOT/admin/voice_transport.go"
grep -q 'first_audio_ready_ms' "$ROOT/admin/voice_chat_stream.go"
grep -q 'OPENJTALK_DICTIONARY_PATH' "$ROOT/voiceworker/piper.go"
grep -q -- '--json-input' "$ROOT/voiceworker/piper_resident.go"
[ -f "$ROOT/docs/VOICE_CONTEXT.md" ] || { echo "voice context documentation missing" >&2; exit 1; }
[ -f "$ROOT/docs/LLM_PROVIDERS.md" ] || { echo "LLM provider documentation missing" >&2; exit 1; }
[ -f "$ROOT/docs/RESIDENT_MODE.md" ] || { echo "resident mode documentation missing" >&2; exit 1; }
if grep -Eq '^[[:space:]]*kill[[:space:]]+-0([[:space:]]|$)' "$ROOT/shared/start-stop.sh"; then
  echo "start-stop.sh must not execute kill -0 for cross-user status detection" >&2
  exit 1
fi
[ -x "$ROOT/x86_64/bin/qnap-voice-worker" ] || { echo "voice worker binary missing" >&2; exit 1; }
[ -f "$ROOT/icons/QnapAssistant.png" ] || { echo "QPKG icon missing" >&2; exit 1; }
[ -f "$ROOT/icons/QnapAssistant_80.png" ] || { echo "QPKG 80px icon missing" >&2; exit 1; }
(cd "$ROOT/admin" && go test ./... && go vet ./...)
echo "Static validation passed."
