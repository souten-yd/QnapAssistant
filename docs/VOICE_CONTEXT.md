# Voice chat request controls

QnapAssistant 0.4.1 keeps the 0.4 audio-only voice API compatible while adding
per-request control of the LLM prompt, output limit and conversation history.
These controls apply to both:

```text
POST /v1/voice/chat
POST /v1/voice/chat/stream
```

The current ASR transcript is always appended by QnapAssistant as the final
`user` message. `history` therefore contains only turns that happened before
the current recording.

## Control object

```json
{
  "system": "You are a small companion. Reply in Japanese in one to three sentences.",
  "max_tokens": 120,
  "history": [
    {"role": "user", "content": "前の質問"},
    {"role": "assistant", "content": "前の回答"}
  ]
}
```

`messages` is accepted as an alias for `history`. History roles are limited to
`user` and `assistant`; use the separate `system` field for the system prompt.
An explicitly empty `system` removes the configured default system message for
that request.

Limits are deliberately bounded before forwarding to llama.cpp:

- system: 8 KiB
- history: 32 messages / 48 KiB total
- max_tokens: 1..2048
- context JSON: 64 KiB

If a control is omitted, the existing QnapAssistant voice configuration remains
in effect (`VOICE_SYSTEM_PROMPT`, `VOICE_REPLY_MAX_TOKENS`, etc.).

## Raw/chunked audio: X-Qnap-Voice-Context

This is the preferred transport for M5GO and relay servers because the request
body remains pure WAV/PCM and may still use HTTP chunked upload.

Encode the control object as UTF-8 JSON, then base64url without padding:

```text
X-Qnap-Voice-Context: eyJzeXN0ZW0iOiIuLi4iLCJtYXhfdG9rZW5zIjoxMjAsImhpc3RvcnkiOltdfQ
```

Example in Python:

```python
import base64, json

context = {
    "system": system_prompt,
    "max_tokens": 120,
    "history": history,
}
encoded = base64.urlsafe_b64encode(
    json.dumps(context, ensure_ascii=False, separators=(",", ":")).encode()
).rstrip(b"=").decode()

headers["X-Qnap-Voice-Context"] = encoded
```

The audio body and `X-Sample-Rate` header are unchanged.

## Query parameters

For diagnostics and simple clients, these now work directly:

```text
?system=<url-encoded text>
&max_tokens=120
&history=<url-encoded JSON array>
```

`history` may alternatively be base64url JSON. The combined context header has
higher precedence than query controls.

This specifically means commands such as:

```text
/v1/voice/chat/stream?profile=m5go&max_tokens=200&system=...
```

are no longer ignored.

## multipart/form-data

Generic clients may put metadata and audio in one request:

```text
part name=context  Content-Type: application/json
part name=audio    Content-Type: audio/wav (or application/octet-stream)
```

A multipart context part overrides the query/header values supplied earlier in
the same request.

## LLM message order

The payload sent to llama.cpp is constructed as:

```text
system override (or VOICE_SYSTEM_PROMPT)
history[0]
history[1]
...
ASR transcript as the current user message
```

Streaming remains unchanged after that point: Qwen SSE output is split into
bounded speech chunks and each chunk is sent to resident Piper as soon as it is
ready.

The `transcript` and `done` timing metadata expose `llm_max_tokens` and
`llm_history_messages`, making it possible to verify from the client that the
requested controls were accepted without echoing the system prompt itself.
