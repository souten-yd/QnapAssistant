# Voice streaming and client profiles (0.4.0)

0.4.0 keeps the existing generic behavior while adding an M5GO-oriented profile for constrained clients.

## Profiles

`VOICE_PROFILE_DEFAULT=generic|m5go` selects the server default. A request can override it with `?profile=m5go` or `X-Qnap-Voice-Profile: m5go`. `/v1/audio/speech` also accepts `"profile":"m5go"` in its JSON body.

Generic defaults preserve the current/native Piper sample rate, do not limit peak level, keep emoji, and use NDJSON/base64 for streaming APIs.

M5GO defaults are 16 kHz mono, a 0.12 peak ceiling, emoji removal, short 8-18 character speech chunks and `multipart/mixed` binary streaming. The 22.05 kHz to 16 kHz conversion uses a windowed-sinc low-pass resampler on the NAS so energy above the destination Nyquist frequency is filtered before downsampling.

All per-profile values are editable through `/api/config` and the Web UI.

## Speech APIs

- `POST /v1/audio/speech`: synchronous WAV. Supports `sample_rate`, `peak` and `profile`. Response includes `X-Qnap-Peak`, `X-Qnap-Sample-Rate`, `X-Qnap-Source-Sample-Rate` and `X-Qnap-Voice-Profile`.
- `POST /v1/audio/speech/stream`: splits a long reply into bounded natural chunks and synthesizes them with the resident Piper process. Generic returns NDJSON/base64; M5GO returns binary `multipart/mixed` audio parts.

## One-call voice chat

- Generic `/v1/voice/chat` remains the legacy JSON + base64 response for compatibility.
- `POST /v1/voice/chat/stream` streams transcript, text chunks, audio chunks and final metadata while Qwen is still generating.
- When the resolved profile is `m5go`, `POST /v1/voice/chat` automatically uses the streaming multipart response instead of producing one large base64 JSON object.

Multipart audio parts have `Content-Length`, `X-Qnap-Sample-Rate`, `X-Qnap-Peak`, `X-Qnap-TTS-Backend` and chunk index headers. An ESP32 client can therefore copy each body directly into its playback ring buffer without retaining the complete part in heap.
