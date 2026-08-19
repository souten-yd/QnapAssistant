# Voice Pipeline: ASR → LLM → TTS

## Goal

QnapAssistant を QNAP TS-253Be 上の音声バックエンドとして拡張し、M5GO などのクライアントは音声入出力だけを担当する。

```text
M5GO
  ├─ mic capture / push-to-talk / lightweight VAD
  ├─ send PCM16 16 kHz mono
  └─ receive/play PCM/WAV
        │
        ▼
QnapAssistant :11435
  ├─ Voice session manager
  ├─ ASR: sherpa-onnx + SenseVoiceSmall INT8 (ja)
  ├─ LLM: llama.cpp + Qwen3-0.6B Q8_0 (Thinking OFF default)
  └─ TTS: sherpa-onnx + Supertonic 3 INT8 (ja)
```

## Why this split

- M5GO/ESP32 は録音、簡易VAD、ボタン、再生には十分だが、日本語汎用ASRや高品質TTSをローカル実行するにはRAM/CPUが不足する。
- TS-253Be は 16 GB RAM 構成で Qwen3-0.6B Q8_0 の実機推論を確認済み。
- ASR と TTS を sherpa-onnx に統一すると、同一の x86_64 CPU ランタイム方針・ONNX Runtime・モデル管理を共有できる。

## Model selection

### ASR default

`SenseVoiceSmall INT8`

- sherpa-onnx SenseVoice offline recognizer
- default language: `ja`
- ITN enabled
- 16 kHz mono PCM/WAV input
- resident by default to avoid speech-start cold latency

Fallback candidates:

1. Whisper tiny INT8/quantized for accuracy comparison
2. Future sherpa-onnx Japanese ASR models only after TS-253Be benchmark

### LLM default

Existing `Qwen3-0.6B-Q8_0.gguf`

- Thinking OFF by default
- current 300 s idle unload remains available
- when a voice session starts, pre-warm llama-server while the user is still speaking so cold-load latency is hidden behind recording time

### TTS default

`sherpa-onnx-supertonic-3-tts-int8-2026-05-11`

- language: `ja`
- CPU INT8
- first benchmark with `num_steps=4` and `threads=2`, then compare quality/latency against 6/8 steps and 4 threads
- resident by default if RAM and measured RTF are acceptable

Fallback rule:

- If Supertonic 3 on J3455 is slower than real time or perceptually too latent, benchmark a lightweight Japanese VITS/Piper-compatible alternative. Do not change default until measured on the physical TS-253Be.

## Residency policy

```text
qnap-assistant manager      always resident
ASR worker/model            resident by default
TTS worker/model            resident by default
llama-server/Qwen           on-demand + idle unload
```

Rationale: ASR must react immediately when speech ends and TTS must begin quickly after LLM output. Qwen currently has a measurable cold-load delay, so a voice session start should trigger asynchronous LLM pre-warm.

Config knobs:

```text
VOICE_ENABLED=1
ASR_RESIDENT=1
TTS_RESIDENT=1
VOICE_PREWARM_LLM=1
ASR_THREADS=4
ASR_LANGUAGE=ja
TTS_THREADS=2
TTS_LANGUAGE=ja
TTS_NUM_STEPS=4
TTS_SPEED=1.0
VOICE_MAX_AUDIO_SECONDS=30
VOICE_SESSION_KEEPALIVE_SECONDS=300
```

## API

### OpenAI-compatible component APIs

```text
POST /v1/audio/transcriptions
POST /v1/audio/speech
```

`/v1/audio/transcriptions` accepts WAV or multipart audio and returns transcript JSON.

`/v1/audio/speech` accepts text JSON and returns WAV/PCM audio.

### End-to-end API

```text
POST /v1/voice/chat
```

MVP request:

- `Content-Type: audio/wav` or `audio/L16`
- query/config controls language, output format, TTS speed and session behavior

The server performs:

```text
audio
  ↓
ASR
  ↓
transcript
  ↓
Qwen3 chat
  ↓
assistant text
  ↓
TTS
  ↓
audio response
```

For the embedded client, the primary response should be streamed binary audio instead of base64 JSON to minimize M5GO RAM use.

A session metadata endpoint exposes text/timings:

```text
GET /v1/voice/sessions/{id}
```

Example metadata:

```json
{
  "id": "...",
  "transcript": "今日の予定を教えて",
  "reply": "今日は…",
  "timings_ms": {
    "asr": 420,
    "llm": 3300,
    "tts": 900,
    "total": 4620
  }
}
```

### Streaming API (phase 2)

```text
WS /v1/voice/stream
```

Client frames:

- JSON `start` with sample rate/format
- binary PCM16 chunks
- JSON `end`

Server events:

- `asr.partial` / `asr.final`
- `llm.text`
- `tts.start`
- binary PCM audio chunks
- `done` with timing metrics

This becomes the preferred M5GO path after the non-streaming MVP is stable.

## M5GO responsibilities

M5GO should not perform full ASR or TTS.

It should perform only:

- microphone capture
- push-to-talk first; wake word later
- simple local VAD/noise gate
- 16 kHz mono PCM16 buffering
- Wi-Fi transport
- response audio playback
- display ASR/assistant text if desired

Recommended first UX:

```text
button down → start voice session / prewarm LLM
record while held
button up → send audio
QNAP ASR → LLM → TTS
stream returned audio to speaker
```

## Worker architecture

Avoid launching a new model process for every request.

Preferred implementation:

```text
qnap-assistant (Go, public :11435)
   │
   ├─ llama-server (localhost :11436, existing)
   │
   └─ qnap-voice-worker (localhost or Unix socket)
          ├─ sherpa-onnx OfflineRecognizer / SenseVoice
          └─ sherpa-onnx OfflineTts / Supertonic 3
```

`qnap-voice-worker` should be a small native C++ service linked against sherpa-onnx C/C++ APIs. It loads ASR/TTS models once and exposes only localhost/Unix-socket RPC to the Go manager.

Build requirements:

- x86_64 CPU-only
- static linking where possible, matching the existing QTS compatibility policy
- no AVX/AVX2 requirement; SSE4.2-safe target for Celeron J3455
- no Python or Node runtime on the NAS
- pin sherpa-onnx commit/version and model URLs/checksums

## Model storage

Models stay outside the QPKG install directory:

```text
/share/Public/QnapAssistant/models/asr/sensevoice/
/share/Public/QnapAssistant/models/tts/supertonic3/
```

The QPKG contains runtime binaries and download/verify scripts only.

## Performance acceptance criteria on TS-253Be

ASR:

- 5 s Japanese utterance RTF <= 0.5 target
- preferred RTF <= 0.3

TTS:

- RTF < 1.0 mandatory for interactive use
- target RTF <= 0.5
- measure time-to-first-audio separately from total synthesis time

Voice turn, warm models:

- target speech-end → first returned audio <= 5 s
- acceptable initial MVP <= 8 s

Cold LLM:

- session-start pre-warm should hide most model-load latency behind user recording

RAM:

- keep enough available memory for QTS services
- ASR/TTS may stay resident only after real RSS is measured

## Benchmark matrix

### ASR

- SenseVoice INT8: threads 2/3/4
- 3 s / 5 s / 10 s Japanese WAV
- report RTF, wall time, RSS, transcript

### TTS

- Supertonic 3 INT8
- `num_steps`: 4 / 6 / 8
- threads: 2 / 4
- short sentence / 2-sentence response
- report RTF, time-to-first-audio, wall time, output duration, RSS

### End-to-end

Measure separately:

```text
upload
ASR
LLM load (if cold)
LLM prompt
LLM generation
TTS
first-audio latency
total
```

## CI cost policy

Do not rebuild sherpa-onnx on every QPKG run.

Use the same pattern as llama.cpp:

1. pinned sherpa-onnx commit/version
2. Actions cache keyed by target profile + commit
3. optional verified baseline artifact reuse
4. expensive voice runtime rebuild only through explicit workflow_dispatch input
5. model files are never bundled into normal CI artifacts

## Delivery phases

### Phase 1 — runtime benchmark

- add pinned sherpa-onnx build/runtime
- add model download + SHA verification
- add ASR benchmark command
- add TTS benchmark command
- run on physical TS-253Be

### Phase 2 — component APIs

- `/v1/audio/transcriptions`
- `/v1/audio/speech`
- `/api/voice/status`
- management UI Voice section

### Phase 3 — end-to-end voice chat

- `/v1/voice/chat`
- LLM pre-warm on voice session start
- timing telemetry
- audio response streaming

### Phase 4 — M5GO client

- push-to-talk MVP
- PCM upload
- streaming playback
- text display

### Phase 5 — conversational streaming

- WebSocket full duplex
- server-side secondary VAD
- optional wake-word flow
- sentence/chunk TTS to reduce perceived latency
