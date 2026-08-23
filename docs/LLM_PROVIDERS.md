# LLM providers

QnapAssistant 0.5 supports two selectable LLM providers while keeping the same
voice/session APIs:

- `local`: the existing bundled llama.cpp server and local GGUF (Qwen3-0.6B by default)
- `openrouter`: OpenRouter's OpenAI-compatible API

Select with `LLM_PROVIDER=local|openrouter` through `/api/config` or the web UI.
Switching provider stops any local llama.cpp process; the next request uses the
new provider. Voice `session_id` history is stored by QnapAssistant and therefore
survives provider changes.

## Recommended OpenRouter setup

The normal web-UI path is intentionally short:

1. Save an OpenRouter API key.
2. Choose a model from the live OpenRouter catalog.
3. Click the no-cost API connectivity check.
4. Optionally run the free generation smoke test.

The model selector is populated from OpenRouter's current model API and shows
free status, context length, catalog prices, current policy eligibility, and
locally recorded voice success/failure health when available.
Provider routing, model fallbacks, privacy filters, sampling controls, presets,
price ceilings, and custom headers are under **Advanced settings** and normally
do not need to be changed.

Fresh installs leave optional provider-routing controls blank so OpenRouter's
own defaults apply. In particular, provider failover is already enabled by
OpenRouter by default. Model-level fallbacks remain opt-in for generic `/v1`
requests because selecting a different model changes model behavior, not merely
the serving provider. Voice streaming has a separate reliability fallback
described below.

## OpenRouter API key

The API key is deliberately excluded from `config.env`, API config responses,
logs, and source control. Configure it with the web UI or:

```sh
curl -X PUT http://NAS:11435/api/openrouter/key \
  -H 'Content-Type: application/json' \
  -d '{"api_key":"<your OpenRouter key>"}'
```

QnapAssistant stores the key separately as:

```text
/share/Public/QnapAssistant/openrouter.key
```

with Unix mode `0600`. `OPENROUTER_API_KEY` in the service environment takes
precedence over the file. `GET /api/openrouter/key` returns only whether a key
is configured and a short SHA-256 fingerprint; it never returns the key.

Delete a stored key with:

```sh
curl -X DELETE http://NAS:11435/api/openrouter/key
```

## Models and policy eligibility

The primary model is `OPENROUTER_MODEL`. `OPENROUTER_FALLBACK_MODELS` is an
ordered comma-separated fallback list used by generic OpenRouter requests.
When fallbacks are configured, QnapAssistant sends the complete ordered model
chain to OpenRouter; otherwise it sends only the primary model.

The initial default is:

```text
OPENROUTER_MODEL=openrouter/free
OPENROUTER_FALLBACK_MODELS=
```

Browse current models from OpenRouter through the NAS without exposing the key
to browser-side third parties:

```sh
# free models only
curl 'http://NAS:11435/api/openrouter/models?free=1'

# all text-output models
curl 'http://NAS:11435/api/openrouter/models?free=0'
```

The response includes model id/name, context length, pricing metadata,
supported parameters, local health statistics and a `policy_status` field.
When an API key is configured, QnapAssistant also calls authenticated
`GET /api/v1/models/user`. OpenRouter defines this endpoint as the model list
after applying the user's provider preferences, privacy settings and guardrails.
QnapAssistant compares it with the public text-model catalog:

- `allowed` / **Policy OK**: the model remains available under the current account policy.
- `filtered` / **Policy除外**: the public model is absent from the user-filtered list.
- `unknown` / **Policy ?**: eligibility could not be checked, for example because the user endpoint was unavailable.

`Policy OK` is a proactive account-policy signal, not a guarantee that every
individual request will succeed. Request-specific provider restrictions, ZDR,
model/provider outages, rate limits, context size and moderation can still make
a later request fail.

## Voice-stream free-model fallback

`/v1/voice/chat/stream` has a separate reliability fallback for OpenRouter.
It is enabled by default and is intentionally limited to failures that happen
before any answer text/audio has been emitted:

```text
OPENROUTER_VOICE_AUTO_FALLBACK=1
OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS=3
```

`OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS` accepts `1..5` and includes the primary
attempt. The Web UI can turn the feature on/off and change the attempt count.

Behavior:

- HTTP `429`: record the failure and skip that model for two minutes. It is not permanently blacklisted because rate limiting is normally transient.
- HTTP `404` with an explicit `guardrail`, `data policy`, `privacy`, `ZDR`, or zero-data-retention signal: record the reason and permanently blacklist that model until cleared.
- Other HTTP `404`: retry another model and put the failed model on a ten-minute cooldown rather than permanently blacklisting it.
- Other status/error classes: record the failure but do not perform this cross-model retry.

When a retry is required, QnapAssistant fetches the authenticated
`/api/v1/models/user` catalog, keeps text-output models that are free, removes
blacklisted/cooling-down/already-attempted models, randomizes the remaining
candidate order, and retries until it gets a reply or reaches the configured
attempt limit. Normal successful primary requests do not perform this extra
catalog call, so the common voice path gets no additional preflight latency.

Provider fallback within the same selected model remains available through
OpenRouter. Cross-model fallback is performed by QnapAssistant so it can keep
accurate per-model health and blacklist state.

The health database is stored separately from configuration at:

```text
/share/Public/QnapAssistant/openrouter-fallback.json
```

with mode `0600`. It records success/failure counts, last failure status/reason,
last success/failure timestamps, cooldown and blacklist state. The Web UI can
clear one blacklisted model or all blacklists. Clearing a blacklist keeps the
historical success/failure counters so reliability history is not lost.

The API is also available directly:

```sh
# inspect fallback state
curl http://NAS:11435/api/openrouter/fallback

# clear one blacklist entry
curl -X POST http://NAS:11435/api/openrouter/fallback \
  -H 'Content-Type: application/json' \
  -d '{"action":"clear_blacklist","model":"vendor/model:free"}'

# clear all blacklist flags (history remains)
curl -X POST http://NAS:11435/api/openrouter/fallback \
  -H 'Content-Type: application/json' \
  -d '{"action":"clear_blacklist","model":""}'
```

The streaming retry layer also recognizes OpenRouter error objects delivered
inside an HTTP-200 SSE stream. It will retry them only if no user-visible text
has been emitted. Once speech/text output starts, QnapAssistant never changes
models mid-answer because doing so would splice two model responses together.

## Provider routing and generation controls

The following optional OpenRouter controls remain available under the collapsed
advanced section:

```text
OPENROUTER_PRESET
OPENROUTER_TEMPERATURE
OPENROUTER_TOP_P
OPENROUTER_REASONING_EFFORT
OPENROUTER_PROVIDER_SORT              # blank|price|throughput|latency
OPENROUTER_ALLOW_FALLBACKS            # blank|0|1
OPENROUTER_REQUIRE_PARAMETERS         # blank|0|1
OPENROUTER_DATA_COLLECTION            # blank|allow|deny
OPENROUTER_ZDR                        # blank|0|1
OPENROUTER_PROVIDER_ONLY              # comma-separated provider slugs
OPENROUTER_PROVIDER_IGNORE            # comma-separated provider slugs
OPENROUTER_MAX_PRICE_PROMPT
OPENROUTER_MAX_PRICE_COMPLETION
OPENROUTER_HTTP_REFERER
OPENROUTER_X_TITLE
```

Blank optional fields are omitted so OpenRouter/model defaults remain in
control. Existing installations keep any advanced values they have explicitly
saved. The UI also provides **Reset to OpenRouter defaults**, which clears the
optional advanced overrides while preserving the selected primary model.

The local-Qwen `THINKING_MODE` and llama.cpp `chat_template_kwargs` are never
forwarded to OpenRouter. OpenRouter reasoning is controlled separately by
`OPENROUTER_REASONING_EFFORT`.

`VOICE_REPLY_MAX_TOKENS=0` continues to mean "do not send max_tokens" for both
providers. A positive per-request or persistent value still sets an explicit
reply cap.

## Connectivity checks

### No-cost API/key check

The preferred web-UI connectivity check calls:

```text
GET /api/openrouter/check
```

QnapAssistant then calls OpenRouter `GET /api/v1/key` with the stored API key.
No completion is generated and no model tokens are consumed. The response
reports a small safe subset of key metadata such as label, usage/limit fields,
free-tier state, and round-trip time; the secret key itself is never returned.

```sh
curl http://NAS:11435/api/openrouter/check
```

### Free generation smoke test

The separate **Free generation test** calls:

```text
POST /api/openrouter/test
```

with an 8-token limit. Unless a caller explicitly sends `allow_paid=true`, this
endpoint forcibly uses `openrouter/free` even if a paid model id was supplied.
This verifies the completion path while avoiding a paid-model selection from the
web UI.

```sh
curl -X POST http://NAS:11435/api/openrouter/test \
  -H 'Content-Type: application/json' \
  -d '{"max_tokens":8}'
```

## Generic `/v1` API

When `LLM_PROVIDER=openrouter`, the existing `/v1/*` API is proxied to
OpenRouter. For `/v1/chat/completions`, QnapAssistant applies the configured
primary/fallback model chain and provider routing settings before forwarding.
Other OpenAI-compatible endpoints are proxied without a request-body rewrite.

When `LLM_PROVIDER=local`, behavior remains the existing local llama.cpp path.
