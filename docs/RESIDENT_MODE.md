# Model residency and auto-unload

QnapAssistant 0.5 manages LLM, ASR and TTS residency independently. The default
keeps the existing low-latency behavior: all primary local models stay hot.
Each model class can instead be unloaded after its own idle timeout.

## Local LLM

```text
LLM_AUTO_UNLOAD=0
LLM_IDLE_TIMEOUT_SECONDS=300
```

`0` keeps the local llama.cpp/Qwen model resident. `1` unloads llama.cpp after
the configured number of idle seconds. Active requests, including a streaming
voice completion, hold an activity lease so the idle loop cannot unload the
model while generation is in progress.

This switch only applies to `LLM_PROVIDER=local`. OpenRouter has no local LLM
weights to unload.

For compatibility with older installations, `KEEP_MODELS_LOADED` and
`IDLE_TIMEOUT_SECONDS` are migrated into the new LLM settings when the new keys
are absent.

## ASR / SenseVoice

```text
ASR_AUTO_UNLOAD=0
ASR_IDLE_TIMEOUT_SECONDS=300
```

When enabled, the SenseVoice recognizer/session is deleted after the timeout.
The voice-worker process stays alive. The next `/asr` request recreates the
recognizer lazily before decoding.

## TTS / Piper Plus and Supertonic

```text
TTS_AUTO_UNLOAD=0
TTS_IDLE_TIMEOUT_SECONDS=300
```

When enabled, the resident Piper process and any loaded Supertonic ONNX session
are released after the timeout. The next TTS request starts/loads only the
backend it needs. Piper remains the primary fast path; Supertonic remains a lazy
fallback.

## Startup behavior

- Local LLM auto-unload disabled: llama.cpp is warmed after provisioning.
- Local LLM auto-unload enabled: llama.cpp stays cold until first use.
- ASR auto-unload disabled: SenseVoice is preloaded when the voice worker starts.
- TTS auto-unload disabled: Piper is prewarmed when the voice worker starts.
- Both ASR and TTS auto-unload enabled: the voice worker itself may remain
  stopped until the first voice request; models are then loaded lazily.

The web UI exposes all three switches and timeouts. Runtime state is visible in
`GET /api/status` and `GET /api/voice/status`.
