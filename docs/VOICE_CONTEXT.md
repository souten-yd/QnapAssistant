# Voice chat context and streaming protocol

QnapAssistant 0.4.2 keeps the audio-only voice API compatible while allowing
clients to control the LLM context and to maintain long-running conversation
sessions without resending the whole conversation on every utterance.

Public endpoints:

```text
POST /v1/voice/chat
POST /v1/voice/chat/stream
GET  /api/voice/protocol
```

`/api/voice/protocol` is the machine-readable capability description. Clients
should prefer it when they need to discover supported controls and current M5
profile defaults.

## 1. What counts as one input utterance

There is **no fixed 30-second boundary** in the voice chat protocol.

One request body is one utterance. The input ends when the client finishes the
HTTP request body. With the M5 PTT flow that means `listen.begin` starts capture
and `listen.end` / PTT release finishes it. A future VAD client can use its own
speech-end decision and finish the body at that point.

QnapAssistant runs ASR over that complete utterance and appends the resulting
transcript as one current `user` message to the LLM context. A 3-second and a
30-second utterance therefore follow the same path.

The request body has a 32 MiB safety bound. At 16 kHz mono PCM16 this is far
longer than a normal spoken turn and is not intended as a conversational time
limit.

## 2. Reply length is independent from audio chunk size

The LLM may produce a long answer. Streaming does **not** impose the M5
8-18-character chunk size as an answer-length limit.

Conceptually:

```text
complete utterance
  -> SenseVoice transcript
  -> Qwen completion (potentially long)
       -> text chunk 1 -> Piper -> WAV part 1 -> M5 plays
       -> text chunk 2 -> Piper -> WAV part 2 -> M5 plays
       -> text chunk 3 -> Piper -> WAV part 3 -> M5 plays
       ... while Qwen keeps generating
```

For the `m5go` profile, `VOICE_M5_CHUNK_MIN_CHARS` and
`VOICE_M5_CHUNK_MAX_CHARS` only tune the latency/prosody unit sent to Piper and
the M5. Sentence-ending punctuation is still a hard boundary even when a
sentence is shorter than the configured minimum.

## 3. Control object

Both voice-chat endpoints accept the following logical control object:

```json
{
  "system": "Answer naturally in Japanese and address the user's request directly.",
  "max_tokens": 400,
  "history": [
    {"role": "user", "content": "previous question"},
    {"role": "assistant", "content": "previous answer"}
  ],
  "session_id": "kizuna-livingroom",
  "reset_session": false
}
```

`messages` is accepted as an alias for `history`. Inline history roles are
`user` and `assistant`; use the separate `system` field for the system prompt.
The current ASR transcript is always appended by QnapAssistant as the final
`user` message, so callers must not duplicate the current utterance in
`history`.

An explicitly empty `system` removes the configured default system message for
that request.

### Limits

- system: 8 KiB
- inline history: 32 messages / 48 KiB total
- explicit `max_tokens`: 1..2048
- control JSON: 64 KiB
- session id: 1..64 ASCII letters, digits, `.`, `_`, `-`

## 4. max_tokens semantics

There is no built-in 200-token voice limit.

When `max_tokens` is supplied by the request, that positive value is forwarded
to the OpenAI-compatible backend. When it is omitted, QnapAssistant consults
`VOICE_REPLY_MAX_TOKENS`:

```text
VOICE_REPLY_MAX_TOKENS=0     -> omit max_tokens; inherit backend standard
VOICE_REPLY_MAX_TOKENS=400   -> use 400 unless request overrides it
```

The default is `0`, so a normal request follows llama.cpp/OpenAI-compatible
backend completion semantics. This is intentionally independent from streaming
chunk sizes.

Telemetry reports `llm_max_tokens: "backend_default"` when no explicit cap was
sent to the backend.

## 5. Long-running sessions

`session_id` enables server-managed rolling conversation state. The same id can
be reused for hundreds of spoken turns without putting hundreds of raw messages
into every LLM request.

QnapAssistant stores sessions under:

```text
/share/Public/QnapAssistant/voice/sessions/<session_id>.json
```

The prompt keeps the most recent 12 messages verbatim. Older messages are moved
into a bounded compact conversation-memory section so prompt size remains
stable as the session grows. Session files expire after seven days of
inactivity. `reset_session=true` discards the previous state before processing
the current utterance.

The completed user transcript and assistant reply are committed to the session
only after a successful LLM completion. Failed/interrupted replies are not
silently added as successful turns.

Response timings expose:

```text
session_id
session_turns
session_recent_messages
session_summary_chars
session_total_messages
session_persisted
```

## 6. Raw or chunked audio: X-Qnap-Voice-Context

This is the preferred metadata transport for M5 and relay servers because the
body remains pure WAV/PCM and can still use HTTP chunked upload.

Encode the control object as UTF-8 JSON and then base64url. Padding is optional:

```text
X-Qnap-Voice-Context: <base64url-json>
```

Example:

```python
import base64, json

context = {
    "system": system_prompt,
    "session_id": "kizuna-livingroom",
    # Leave max_tokens out to inherit the backend standard.
}
encoded = base64.urlsafe_b64encode(
    json.dumps(context, ensure_ascii=False, separators=(",", ":")).encode()
).rstrip(b"=").decode()
headers["X-Qnap-Voice-Context"] = encoded
```

The audio request body and `X-Sample-Rate` are otherwise unchanged.

## 7. Query controls

Diagnostics and simple clients can use query parameters:

```text
?system=<url-encoded text>
&max_tokens=400
&history=<url-encoded JSON array>
&session_id=kizuna-livingroom
&reset_session=0
&profile=m5go
```

`history` may also be base64url JSON. Query `system`, `max_tokens`, and
`history` are no longer ignored.

Precedence for context values is:

```text
persistent defaults < query < X-Qnap-Voice-Context < multipart context
```

## 8. multipart request input

Generic clients may send metadata and audio in one multipart request:

```text
part name=context  Content-Type: application/json
part name=audio    Content-Type: audio/wav (or application/octet-stream)
```

The multipart context object has the same fields as the header object.

## 9. LLM message order

Without a server session:

```text
system override (or VOICE_SYSTEM_PROMPT)
inline history
current ASR transcript as user
```

With `session_id`:

```text
system override (or VOICE_SYSTEM_PROMPT)
compact old-session memory, when present
recent session messages
optional inline history supplied for this request
current ASR transcript as user
```

Qwen3 Thinking OFF behavior remains controlled independently and does not alter
these context semantics.

## 10. Streaming response

Qwen is read as an SSE stream. As soon as a natural text unit becomes ready,
that unit is sent to resident Piper. Qwen continues generating later content
while earlier audio is synthesized and sent.

The `m5go` profile returns `multipart/mixed` with metadata and raw `audio/wav`
parts, avoiding a whole base64 reply in ESP32 heap. Generic streaming can use
NDJSON/base64 for compatibility.

Important timing fields include:

```text
asr_wall_ms
llm_first_token_ms
first_text_chunk_ms
first_audio_ready_ms
stream_chunks
llm_max_tokens
llm_history_messages
total_ms
```

The protocol endpoint documents the active profile values at runtime:

```sh
curl http://127.0.0.1:11435/api/voice/protocol
```
