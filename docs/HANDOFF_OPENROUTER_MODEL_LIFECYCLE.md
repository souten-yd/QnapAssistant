# Handoff: OpenRouter provider + independent model lifecycle controls

Status date: 2026-08-23

Working branch: `feat/openrouter-model-lifecycle`

Baseline: `main` at `89259a0c8c3c65b7c2a17988265c7d455a087c6d` (`Release v0.4.2 [release]`).

This document is the authoritative handoff for the next implementation session. Read it together with `docs/VOICE_CONTEXT.md`, `docs/VOICE_STREAMING_0.4.md`, `admin/manager.go`, `admin/voice.go`, `admin/voice_chat_context.go`, `admin/voice_chat_session.go`, `admin/voice_session.go`, `admin/config.go`, `admin/config_handler_v04.go`, `admin/ui.go`, and the `voiceworker/` package before changing code.

## 1. User goals

Add two related capabilities without regressing the existing v0.4.2 M5GO voice path.

### A. Independent automatic unloading

The user must be able to enable/disable automatic unloading independently for:

- local LLM (llama.cpp / Qwen3-0.6B),
- ASR (SenseVoice),
- TTS (Piper Plus; Supertonic remains fallback and should follow the same TTS lifecycle semantics unless a better separation is justified).

Each component should have a configurable idle timeout when auto-unload is enabled. A value of `0` should not be overloaded to mean both “disabled” and “immediate”; use an explicit enable flag plus an idle timeout.

The existing global `KEEP_MODELS_LOADED` / `IDLE_TIMEOUT_SECONDS` behavior is legacy compatibility. Migrate cleanly without silently changing an existing installation. Prefer explicit new keys and a compatibility mapping when new keys are absent.

Target configuration shape (names may be adjusted during implementation, but keep the concepts separate):

```text
LLM_AUTO_UNLOAD=0
LLM_IDLE_TIMEOUT_SECONDS=300
ASR_AUTO_UNLOAD=0
ASR_IDLE_TIMEOUT_SECONDS=300
TTS_AUTO_UNLOAD=0
TTS_IDLE_TIMEOUT_SECONDS=300
```

Requirements:

- Local LLM unload must stop only llama.cpp.
- ASR unload must release the SenseVoice runtime/session without unnecessarily killing TTS.
- TTS unload must release resident Piper and any loaded Supertonic state without unnecessarily killing ASR.
- A subsequent request must lazily reload the needed component.
- Status API/UI must show loaded/unloaded state and auto-unload settings for all three.
- Manual start/stop/restart actions should remain possible where they make sense.
- OpenRouter has no local model process, so local LLM auto-unload is “not applicable” while the provider is OpenRouter; do not create fake unloading behavior for the remote provider.

Important architecture note: today `qnap-voice-worker` owns ASR and TTS in one process. Merely stopping the worker cannot satisfy independent ASR/TTS unload. Implement actual per-engine lifecycle inside the worker (or an equally clean design) rather than presenting independent UI toggles that all stop the same process.

## 2. OpenRouter provider

Add an LLM provider selector so the user can switch between:

- `local`: existing llama.cpp + Qwen3-0.6B behavior,
- `openrouter`: OpenRouter API using an API key.

The selected provider must be used consistently by the voice-chat LLM path. Also inspect the public `/v1/*` behavior and decide/document whether `/v1/chat/completions` follows the selected provider or remains a local-only compatibility proxy; avoid two surprising definitions of “selected LLM”. Prefer one shared provider abstraction if practical.

### OpenRouter configuration requirements

At minimum provide editable settings for:

```text
LLM_PROVIDER=local|openrouter
OPENROUTER_API_KEY=<secret>
OPENROUTER_BASE_URL=https://openrouter.ai/api/v1
OPENROUTER_MODEL=<model id>
```

Also expose useful model-generation/routing controls supported by the current OpenRouter API. Do **not** guess the current parameter set: verify against current official OpenRouter documentation before implementation. Candidate controls to investigate include temperature, top_p, max_tokens (0/omitted = provider/model default), reasoning controls, provider routing, fallbacks/multiple models, and any supported-parameter metadata returned by the models API.

The user explicitly wants multiple model selection / detailed settings if OpenRouter supports them. Prefer a model catalog fetched from OpenRouter rather than a hard-coded list. The UI should make model ID, context length, pricing/free status, and relevant supported parameters visible when the API supplies them.

A useful API surface would include endpoints conceptually equivalent to:

```text
GET  /api/llm/provider
PUT  /api/llm/provider
GET  /api/openrouter/models
POST /api/openrouter/test
```

Exact paths may be adjusted to fit the existing API conventions.

### API-key handling (mandatory)

A live OpenRouter API key was supplied by the user in the chat that created this handoff. It is intentionally **not** written here, not committed, and must never be placed in Git, test fixtures, screenshots, logs, PR bodies, CI environment committed in YAML, or API responses.

Implement secret handling as follows:

- Web UI uses a password/secret input.
- `GET /api/config` and status endpoints never return the raw key; return a boolean such as `openrouter_api_key_set` or a masked value with no recoverable secret content.
- Updating other settings with an empty API-key field must preserve the existing key rather than erase it accidentally.
- Persistent secret storage should be outside the QPKG payload in the existing persistent config area and should use restrictive file permissions where QTS allows it.
- Never log request `Authorization` headers or the key.
- Unit/integration tests use injected fake credentials and an `httptest`/mock OpenRouter server.
- A real-network smoke test may use a runtime-injected credential only.

Because the user said the test credential has only about USD 1 available, real-network tests must use a free OpenRouter model when available, or otherwise a minimal prompt and very small output. One successful completion is enough. Do not run loops, benchmarks, or repeated paid calls.

If a future chat does not have access to the original credential, do not try to recover it from repository/history because it must not be there. Complete mock tests first; for a live test, have the user inject the key through the new UI/API or provide it through an ephemeral runtime secret channel.

## 3. Existing v0.4.2 behavior that must not regress

QnapAssistant v0.4.2 already provides:

- `/v1/voice/chat` and `/v1/voice/chat/stream`,
- M5GO multipart streaming with small TTS chunks,
- `system`, optional `max_tokens`, `history/messages`, `session_id`, `reset_session`,
- `VOICE_REPLY_MAX_TOKENS=0` meaning omit `max_tokens` and use backend default,
- persistent server-managed voice sessions,
- recent session messages retained verbatim and older messages compacted into bounded memory,
- `GET /api/voice/protocol`,
- local Qwen3-0.6B Thinking OFF behavior,
- SenseVoice ASR,
- resident Piper Plus and lazy Supertonic fallback,
- generic and M5GO voice profiles.

The M5GO response chunk size is a transport/TTS boundary, not an LLM answer-length cap. Do not reintroduce a short fixed reply limit.

The current default config on main has `VOICE_REPLY_MAX_TOKENS=0`.

## 4. Existing lifecycle implementation to inspect

### Local LLM

`admin/manager.go` starts/stops `llama-server` and currently uses the legacy pair:

```text
KEEP_MODELS_LOADED
IDLE_TIMEOUT_SECONDS
```

`idleLoop()` currently decides whether to unload the whole local LLM process.

Refactor this into an explicit local-LLM lifecycle policy while preserving old config behavior when the new keys are not present.

### ASR/TTS

`admin/voice.go` starts one `qnap-voice-worker` process. The worker currently keeps SenseVoice loaded and Piper resident, while Supertonic is lazy fallback.

To satisfy independent ASR/TTS auto-unload, inspect `voiceworker/` and introduce separate last-used/load/unload state for ASR and TTS. The manager process can own configuration and expose API/UI, but the worker should own the actual model/session teardown and lazy reload.

Do not implement independent toggles by repeatedly killing/restarting the entire voice worker, because that unloads both engines and defeats the requirement.

## 5. LLM provider architecture

Prefer one small provider abstraction used by voice-chat paths rather than sprinkling `if openrouter` checks through streaming and synchronous handlers.

Suggested responsibilities:

```text
LLMProvider
  Chat(ctx, request) -> completion
  StreamChat(ctx, request) -> deltas + metadata
  Name()/Status()
```

Implementations:

```text
LocalLlamaProvider
OpenRouterProvider
```

The request structure should carry the existing voice controls: messages/system-derived messages, optional max_tokens, temperature, and future provider-specific options without breaking local behavior.

OpenRouter streaming should preserve the current QnapAssistant contract: LLM deltas continue to feed the existing text chunker, Piper synthesizes completed chunks, and M5GO receives multipart WAV parts before the full answer is complete.

Provider-specific response metadata should be normalized so existing timing/finish-reason observability keeps working. Preserve useful OpenRouter fields separately where helpful (model actually used, provider if supplied, usage/cost if available), but do not make M5 clients depend on them.

## 6. OpenRouter model catalog / multiple models

Before coding, consult current official OpenRouter API documentation. Confirm:

- model-list endpoint and response schema,
- how free models are identified,
- current support for a fallback/multiple-model list,
- provider-routing object and fields,
- reasoning parameter behavior,
- supported-parameters metadata,
- usage/cost response fields,
- streaming SSE format and error semantics.

Then expose only verified controls.

UI goals:

- Local / OpenRouter provider selector.
- OpenRouter API-key set/change control without displaying the secret.
- Refreshable model catalog.
- Search/filter model list.
- Prefer/show free models for low-cost testing.
- Primary model selection.
- Multiple/fallback model selection only if current OpenRouter semantics support it.
- Advanced section for verified generation/routing options.
- Test-connection button with intentionally short output.
- Clear indication when a setting is provider-specific or unsupported by the chosen model.

Do not hard-code one transient “best free model”; free availability changes. Use the live model catalog and let the user select.

## 7. Config/API compatibility

The current `admin/config_handler_v04.go` allowlists config keys. Extend it deliberately for the new settings.

Secret-bearing config needs a redacted public representation. Do not simply add `OPENROUTER_API_KEY` to the existing GET response and expose it to the LAN UI.

Changing provider from local to OpenRouter should stop the local llama.cpp process if it is no longer needed. Switching back to local should lazy-start llama.cpp on the next request (or immediately only if the user explicitly requests start/preload).

Provider-setting changes should not restart ASR/TTS unless those settings actually changed.

## 8. Testing plan

### Unit tests

Add Go tests for:

- provider selection and defaults,
- redaction/preservation of API key,
- OpenRouter request serialization,
- omitted `max_tokens` when configured as 0/unset,
- OpenRouter SSE parsing,
- OpenRouter error/status propagation without leaking Authorization,
- model catalog parsing/filtering,
- multiple/fallback model request mapping if verified by official docs,
- local provider regression,
- lifecycle config compatibility mapping,
- independent ASR/TTS idle-state decisions.

Use `httptest.Server` for OpenRouter tests. No real credential in tests.

### Existing regression tests

Run:

```text
cd admin && go test ./... && go vet ./...
./scripts/validate.sh
```

Preserve the existing QPKG CI behavior. Do not trigger an expensive llama.cpp rebuild unless the verified runtime cache/baseline is unavailable and rebuilding is explicitly required. Existing CI is designed to reuse the pinned runtime.

### Real OpenRouter smoke test

After mock tests pass:

1. Inject API key at runtime only.
2. Fetch live model catalog.
3. Choose a free model if available.
4. Send one tiny prompt.
5. Keep output very small.
6. Verify both non-stream or stream once; do not benchmark or repeat unnecessarily.
7. Confirm key is absent from logs and API responses.

### Physical QNAP/M5 regression

After QPKG install:

- local Qwen path still works,
- OpenRouter voice path produces text chunks that flow through Piper and M5 multipart playback,
- switching provider does not affect ASR/TTS unexpectedly,
- independent ASR/TTS unload/reload works,
- local LLM unload/reload works when local provider is selected,
- session_id behavior remains intact across providers,
- M5 first-audio streaming remains progressive rather than whole-answer buffering.

## 9. Acceptance criteria

Do not mark the PR ready until all of the following are true:

1. User can select local Qwen or OpenRouter from UI/API.
2. OpenRouter API key can be set/changed without being returned or logged.
3. Model catalog is fetched dynamically and model selection works.
4. Verified advanced OpenRouter settings are configurable.
5. Multiple/fallback models are supported if the current official API supports them.
6. Voice sync and stream paths use the selected provider.
7. No fixed answer-length cap is reintroduced; `max_tokens=0/unset` means provider/backend default.
8. LLM, ASR, and TTS have genuinely independent auto-unload enable/timeout controls.
9. ASR/TTS lazy reload works after independent unload.
10. Existing v0.4.2 session/multipart behavior passes regression tests.
11. Mock tests pass without secrets.
12. One minimal live OpenRouter smoke test passes with no secret leakage.
13. CI succeeds without unnecessary heavy llama rebuild.
14. Protocol/config documentation is updated before merge/release.

## 10. Security / cost constraints

- Never commit the OpenRouter API key.
- Never repeat the key in PR descriptions, logs, docs, test output, or assistant summaries.
- Treat `/api/config` as LAN-visible and redact secrets accordingly.
- Keep real OpenRouter testing to the minimum required for proof of connectivity.
- Prefer free models for smoke testing.

## 11. Recommended implementation order

1. Inspect `voiceworker/` model/session ownership and design true independent ASR/TTS unload.
2. Verify current official OpenRouter docs/API.
3. Add configuration schema + secret-safe GET/PUT behavior.
4. Add shared local/OpenRouter LLM provider abstraction.
5. Route synchronous voice chat through it.
6. Route streaming voice chat through it and preserve chunked TTS behavior.
7. Add OpenRouter model catalog + advanced settings API.
8. Add UI controls.
9. Implement ASR/TTS worker lifecycle endpoints + idle timers + status.
10. Add tests and docs.
11. Run mock/local CI.
12. Perform one minimal live OpenRouter smoke test.
13. Physical QNAP/M5 regression.
14. Squash noisy implementation history if necessary, update PR, then merge/release only after gates pass.

## 12. Important non-goals / cautions

- Do not remove the local Qwen path.
- Do not make OpenRouter mandatory for QPKG startup.
- Do not put secrets into QPKG build artifacts.
- Do not make M5 know anything about OpenRouter; M5 continues to speak the existing QnapAssistant voice protocol.
- Do not couple LLM answer length to M5 TTS chunk length.
- Do not replace `session_id` with client-resubmitted full history for normal long-running voice sessions.
- Do not reintroduce expression tags such as `[[happy]]` into the NAS voice system prompt. Kizuna has a tag-free `voice_system_prompt()` for the NAS path because NAS-side TTS would otherwise read the tags aloud.
