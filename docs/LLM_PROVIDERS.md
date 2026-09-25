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
4. Optionally enable free-model automatic fallback.
5. Optionally run the free generation smoke test.

The model selector is populated from OpenRouter's current model API and shows
free status, context length, pricing, current policy availability and provider
moderation metadata when available. Provider routing, manual model fallbacks,
privacy filters, sampling controls, presets, price ceilings, and custom headers
remain under **Advanced settings**.

Fresh installs leave optional provider-routing controls blank so OpenRouter's
own defaults apply. In particular, provider failover is already enabled by
OpenRouter by default. Cross-model fallback is separate because selecting a
different model can change answer quality and behavior.

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

## Models and policy preflight

The primary model is `OPENROUTER_MODEL`. `OPENROUTER_FALLBACK_MODELS` is an
ordered comma-separated manual fallback list.

The initial default is:

```text
OPENROUTER_MODEL=openrouter/free
OPENROUTER_FALLBACK_MODELS=
OPENROUTER_AUTO_FREE_FALLBACK=0
OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS=3
```

Browse current models from OpenRouter through the NAS without exposing the key
to browser-side third parties:

```sh
# free models only
curl 'http://NAS:11435/api/openrouter/models?free=1'

# all text-output models
curl 'http://NAS:11435/api/openrouter/models?free=0'
```

QnapAssistant compares OpenRouter's public `/models` catalog with authenticated
`/models/user`. OpenRouter documents `/models/user` as filtered by the current
API key's provider preferences, privacy settings and guardrails. Models that
exist in the full catalog but not the user-filtered catalog are returned with:

```text
policy_checked=true
policy_allowed=false
policy_status=Privacy/Guardrail制限
```

The web selector keeps these entries visible for diagnosis but disables them as
choices. Models that survive the filter show `利用可`. `top_provider.is_moderated`
is also surfaced as provider-moderation metadata.

This preflight is intentionally not presented as a guarantee. It can detect
model/provider/privacy policy filtering known at catalog time, but it cannot
predict:

- transient upstream/shared-pool rate limits such as HTTP 429,
- prompt-dependent guardrails such as prompt-injection/DLP/content filters,
- provider failures that occur after the catalog was fetched,
- every request-specific constraint whose exact compatibility is decided only
  during routing.

Therefore policy status means "eligible under the current catalog-visible
policy", not "this request is guaranteed to succeed".

## Automatic free-model fallback

`OPENROUTER_AUTO_FREE_FALLBACK=1` enables cross-model fallback while keeping all
fallback models free. `OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS` is the total
number of model attempts including the primary and is restricted to `1..5`.
The web UI defaults to three total attempts.

QnapAssistant does **not** resend the same HTTP request up to five times. It
builds OpenRouter's native ordered `models` array and lets OpenRouter perform
provider failover and model fallback server-side. This is preferable because
OpenRouter can distinguish provider availability, rate limiting and routing
policy failures without multiple application-level request loops.

Automatic fallback candidates are obtained from authenticated `/models/user`,
so models already excluded by current privacy/provider/guardrail policy are not
inserted into the chain. The order is:

1. configured primary model,
2. entries from `OPENROUTER_FALLBACK_MODELS` that are currently policy-allowed
   and free,
3. other policy-allowed free text models,
4. `openrouter/free` as a final dynamic free-only floor if space remains.

Candidates that cannot satisfy request-level capabilities such as tools,
`tool_choice`, structured output or reasoning are skipped when the catalog
reports the required supported-parameter metadata.

The automatic feature is OFF by default. This is deliberate: changing models
can change answer style and quality, and failed free-model attempts may still
consume request quota. Users who value availability can enable it; users who
need deterministic model identity can leave it disabled and use only provider
failover or a manual model chain.

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
optional advanced overrides while preserving the selected primary model and the
separate automatic-free-fallback preference.

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
reports a safe subset of key metadata such as label, daily/weekly/monthly/total
usage, BYOK usage, limit, remaining balance, reset policy, tier, expiry and
round-trip time; the secret key itself is never returned.

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
When automatic free fallback is enabled, the generated native `models` array
replaces the unrestricted manual chain for that request so paid/manual-blocked
fallbacks cannot leak into free-only mode.

Other OpenAI-compatible endpoints are proxied without a request-body rewrite.
When `LLM_PROVIDER=local`, behavior remains the existing local llama.cpp path.

## Local GGUF download presets

The Local GGUF models card includes **Ornith 1.0 9B Q4_K_M**
(`ornith-1.0-9b-Q4_K_M.gguf`, approximately 5.63 GB) from the
[publisher's GGUF repository](https://huggingface.co/ornith-ai/Ornith-1.0-9B-GGUF).
The preset pins revision `d6b5f9b2ef835df5085615e0c405ce0092cc8b53` and supplies
SHA-256 `5720d1f671b4996481274fffe01868c3c36e87c135cc8538471cc7bd6087b106`
to the existing download API for verification before installation.

1. Select the download preset to fill URL, filename and SHA-256.
2. Click **Download to Public** and wait for completion.
3. Select the downloaded file from the local model list and click **Select local model**.
4. Use **Load / Check** to load it.

Choosing a preset does not download, load or switch models automatically. Custom
URLs remain supported; selecting the custom option clears all three download
fields, including the preset checksum. Downloads use `MODEL_DIR` (normally
`/share/Public`). The installed default and existing user configuration remain
unchanged.

Ornith is a reasoning model specialized for agentic coding. The Local Qwen3
Thinking switch does not apply to its filename. Its download size is not total
runtime memory usage, and NAS inference speed/quality have not been measured.
This preset does not update the bundled llama.cpp runtime or enable tool execution.

Smaller alternatives are also available in the same selector, using Unsloth GGUFs:

| Model (Q4_K_M) | Download size | Intended choice |
| --- | --- | --- |
| Qwen3-4B-Instruct-2507 | 2.50 GB | Non-thinking conversation; try first when prioritizing quality |
| Qwen3-1.7B | 1.11 GB | Lower memory/compute needs; quality must be assessed on your tasks |

- [Qwen3-4B-Instruct-2507](https://huggingface.co/unsloth/Qwen3-4B-Instruct-2507-GGUF): revision `18727206c51467496bfba014368bd0a30e97f411`, SHA-256 `3605803b982cb64aead44f6c1b2ae36e3acdb41d8e46c8a94c6533bc4c67e597`.
- [Qwen3-1.7B](https://huggingface.co/unsloth/Qwen3-1.7B-GGUF): revision `bd59ef4c1c7af8b7ade0d473f3ab0d48b9f1d338`, SHA-256 `b139949c5bd74937ad8ed8c8cf3d9ffb1e99c866c823204dc42c0d91fa181897`.

For 4B Instruct, use Thinking **passthrough** (it is a non-thinking model); for 1.7B, start with Thinking **off** to limit response latency. Preset selection does not change this setting.

### Download status and model selection

`GET /api/models/download` returns the current in-memory transfer status:
`idle`, `connecting`, `downloading`, `verifying`, `complete`, or `failed`, plus
filename/path, written/total bytes, average bytes/second, timestamps and errors.
An unknown total is shown without a percentage. Status resets on service restart;
completed model files remain discoverable via `GET /api/models`.

The UI polls download status independently every second, so voice/provider errors
cannot prevent progress display. On completion the downloaded model is highlighted
in the local list; **Select local model** explicitly saves the selection. List
refresh preserves a user's pending choice instead of resetting it every five seconds.

Hugging Face `/blob/` URLs are converted to `/resolve/`. HTTP errors, invalid GGUF
headers, truncated transfers, checksum mismatches and filesystem errors are shown.
The downloader follows redirects with TLS verification enabled, limits response
header waits to 30 seconds, and aborts after 60 seconds without body data. Failed
partial files are removed and existing installed files are preserved. Click
**Download to Public** again to retry from the beginning; resume is not implemented.
