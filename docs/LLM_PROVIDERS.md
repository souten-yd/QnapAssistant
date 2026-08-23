# LLM providers

QnapAssistant 0.5 supports two selectable LLM providers while keeping the same
voice/session APIs:

- `local`: the existing bundled llama.cpp server and local GGUF (Qwen3-0.6B by default)
- `openrouter`: OpenRouter's OpenAI-compatible API

Select with `LLM_PROVIDER=local|openrouter` through `/api/config` or the web UI.
Switching provider stops any local llama.cpp process; the next request uses the
new provider. Voice `session_id` history is stored by QnapAssistant and therefore
survives provider changes.

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

## Models

The primary model is `OPENROUTER_MODEL`. `OPENROUTER_FALLBACK_MODELS` is an
ordered comma-separated fallback list. When fallbacks are configured,
QnapAssistant sends the complete ordered model chain to OpenRouter; otherwise
it sends only the primary model.

The initial default is:

```text
OPENROUTER_MODEL=openrouter/free
OPENROUTER_FALLBACK_MODELS=
```

Browse current models from OpenRouter through the NAS without exposing the key
to a browser-side third party:

```sh
# free models only
curl 'http://NAS:11435/api/openrouter/models?free=1'

# all text-output models
curl 'http://NAS:11435/api/openrouter/models?free=0'
```

The response includes model id/name, context length, pricing metadata and
supported parameters when OpenRouter reports them.

## Provider routing and generation controls

The web UI exposes the following optional OpenRouter controls:

```text
OPENROUTER_PRESET
OPENROUTER_TEMPERATURE
OPENROUTER_TOP_P
OPENROUTER_REASONING_EFFORT
OPENROUTER_PROVIDER_SORT              # blank|price|throughput|latency
OPENROUTER_ALLOW_FALLBACKS            # 0|1
OPENROUTER_REQUIRE_PARAMETERS         # 0|1
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
control. The local-Qwen `THINKING_MODE` and llama.cpp
`chat_template_kwargs` are never forwarded to OpenRouter. OpenRouter reasoning
is controlled separately by `OPENROUTER_REASONING_EFFORT`.

`VOICE_REPLY_MAX_TOKENS=0` continues to mean "do not send max_tokens" for both
providers. A positive per-request or persistent value still sets an explicit
reply cap.

## Safe connectivity test

The UI's **Safe free test** calls:

```text
POST /api/openrouter/test
```

with an 8-token limit. Unless a caller explicitly sends `allow_paid=true`, this
endpoint forcibly uses `openrouter/free` even if a paid model id was supplied.
This is intended for validating API-key/auth/network connectivity with minimal
or zero model cost.

Example:

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
