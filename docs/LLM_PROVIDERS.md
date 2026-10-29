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

**CPUs without AVX (TS-253Be / Celeron J3455):** the bundled llama.cpp is built for SSE4.2 only.
On-device logs showed Qwen3-4B-Instruct-2507 Q4_K_M reading prompts at about 0.3 tok/s and generating
at about 0.1 tok/s, while Qwen3-0.6B Q8_0 read at 9-16 tok/s. K-quants appear to lack a fast path
without AVX. The selector therefore also offers **Qwen3-4B-Instruct-2507 Q4_0** from the same pinned
Unsloth revision (`Qwen3-4B-Instruct-2507-Q4_0.gguf`, about 2.4 GB). Its SHA-256 is not yet pinned,
so the preset leaves the hash empty and the download is not verified. Fill in the published hash
from the Hugging Face file page before downloading if verification is required. Compare speeds
with `llama-bench` on the NAS before relying on it for SelfCare nightly advice.

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

### SelfCare weekly health analysis: Qwen3-8B Q4_K_M (16 GB RAM) / Q5_K_M

For the requested **16 GB installed RAM** configuration, prioritize the official
**Qwen3-8B Q4_K_M GGUF** (about 5.03 decimal GB, Apache-2.0).
Pinned revision: `7c41481f57cb95916b40956ab2f0b139b296d974`.
Published SHA-256: `d98cdcbd03e17ce47681435b5150e34c1417f50b5c0019dd560e4882c5745785`.
Official file: <https://huggingface.co/Qwen/Qwen3-8B-GGUF/blob/7c41481f57cb95916b40956ab2f0b139b296d974/Qwen3-8B-Q4_K_M.gguf>.

Target a **16,384-token context** for one-week summarized SelfCare analysis:
1. Download Q4 from the preset and wait for successful hash verification.
2. Select `Qwen3-8B-Q4_K_M.gguf` using **Select local model**.
3. Under **Local llama.cpp settings**, set `CONTEXT=16384` and click **Save local LLM settings**.
4. Set Qwen3 Thinking **off** initially to conserve generation time (this is independent of context length), then use **Load / Check**. Confirm readiness and available memory on the NAS.
5. In SelfCare choose QnapAssistant with model `auto`, and run a short AI connectivity test before a weekly consultation.

These are **manual, opt-in actions**: preset selection must not auto-download,
auto-select, start inference, change context, or switch the active model. The
normal installed defaults (`CONTEXT=4096`, Qwen3-0.6B) remain unchanged.

**Important memory limit:** Q4 weights use about **5.03 decimal GB** on disk,
and FP16 KV cache at 16K about **2.25 GiB**, plus compute buffers, QTS,
SelfCare, other QPKGs and filesystem cache. At 16 GB installed RAM, the
Q4/16K configuration is substantially more practical than on a 10 GB NAS,
but installed capacity is not equal to `MemAvailable` and successful startup
is not guaranteed. Approximate combined weight-file size and FP16 KV allocation
are 5.03 decimal GB + 2.25 GiB (about 6.9 GiB combined, or 7.45 decimal GB), **before** runtime
buffers and QTS/application overhead. Inspect available RAM and startup logs;
avoid depending on HDD swap for normal operation. If 16K cannot be sustained,
reduce competing memory use or try a 4B model at 16K to retain the weekly
summary. SelfCare currently has a 300 s consultation timeout and a slow CPU
may exceed it.

The download selector also offers the official **Qwen3-8B Q5_K_M GGUF** (5.85 GB,
Apache-2.0), aimed at Japanese summaries of measurements, food and activity.
Official GGUF: <https://huggingface.co/Qwen/Qwen3-8B-GGUF/blob/7c41481f57cb95916b40956ab2f0b139b296d974/Qwen3-8B-Q5_K_M.gguf>.
Pinned revision: `7c41481f57cb95916b40956ab2f0b139b296d974`.
Verified file SHA-256: `068bae163faa96ad48032daf4e071a6a28fe67d8dcc95367609c2ff165e52738`.
This is a download **option**, not a new default or medical-quality certification.

A **one-week, summarized** SelfCare payload can be budgeted approximately as:

| One-week information | Estimated input tokens |
| --- | ---: |
| Body composition | 500–900 |
| Blood pressure / pulse | 500–1,000 |
| Steps, exercise and energy | 700–1,200 |
| Sleep duration and sleep-stage intervals | 1,000–2,500 |
| Heart rate / SpO2 daily statistics and flagged changes | 700–1,500 |
| Meal logs and calories | 1,500–3,000 |
| Goals, historic comparison and instructions | 1,000–2,000 |
| **Total** | **5,900–12,100** |

These are **planning estimates**, not measured token counts. Actual input can
exceed them with verbose Japanese meal notes, raw measurements, or repeated
JSON keys. Reserve around **2,048 output tokens**, plus chat-template and
system-prompt overhead; cap/count tokens before sending. For a summarized week,
**CONTEXT=16384** is a reasonable *starting* local llama.cpp setting, whereas
the existing global default **CONTEXT=4096** remains untouched. If the measured
prompt would not fit, aggregate further or explicitly use a larger context only
when hardware permits. Raw five-minute heart-rate samples can add 2,016 weekly
records before sleep and other inputs; this is *not* an appropriate input for
16K without downsampling. Do not silently truncate values important to safety.

Qwen3-8B base configuration has 36 layers, 8 KV heads and 128 head dimensions.
At FP16 KV precision and 16,384 context positions, the KV cache alone is
`16384 * 36 * 8 * 128 * 2 (K/V) * 2 bytes = 2,415,919,104 bytes = 2.25 GiB`,
in addition to model weights (~5.03 **decimal GB** for Q4_K_M, or
~5.85 **decimal GB** for Q5_K_M on disk), llama runtime, OS and other
running services. 32,768 context requires ~4.50 GiB FP16 KV cache.
Actual memory may vary with llama.cpp cache quantization, allocation and usage.
A NAS with 8 GB total RAM may be unable to run this model at 16K; consider the
existing Qwen3-4B or 1.7B choices, smaller context or more RAM. Model download
size below 10 GB does **not** establish memory compatibility or acceptable speed.

**Current integration scope:** QnapSelfCare's `wellness_ai.py` passes recent
activity entries (up to seven), the latest measurements, up to five meal notes,
summary and planning data to QnapAssistant. It does **not** yet send a full week
of Apple Health heart-rate/SpO2/sleep samples to the AI. A future SelfCare-side
feature must explicitly build a bounded, user-consented weekly summary, preserve
source timestamps and missing-data flags, and keep health data local when using
the local provider. Model choice does not automatically change that payload.
For a QnapAssistant local model with QnapSelfCare, select the local provider and
choose `model=auto` in SelfCare. Health advice is not a diagnosis or a reason
to change medication; review changes with an appropriate clinician.

### Local model input / output playground

The QnapAssistant management page provides a **ローカルAI 入出力テスト**
card for manually testing the currently selected **local** GGUF model (for
example Qwen3-8B Q4_K_M with `CONTEXT=16384`).

- Enter optional system instructions and paste an arbitrary Japanese question,
  a bounded seven-day SelfCare summary, or an already prepared JSON snippet.
- **回答を生成** sends a single non-streaming request to
  `POST /api/llm/local-test`; the response area shows the raw text plus
  elapsed time, input/output token counts when supplied by llama.cpp, and
  the finish reason. Long-running requests can be **中止**-ed from the browser.
- **架空の7日データを入力** inserts only synthetic sample measurements. It
  does not query SelfCare or the Apple Health sample database.
- Only the selected **local** backend (`127.0.0.1:BACKEND_PORT`) is used.
  The server rejects the endpoint when `LLM_PROVIDER=openrouter`, refuses
  provider-aware fallback/remote forwarding and does not follow redirects.
  This prevents accidental transmission to an external LLM provider even
  when the provider selection changes after opening the page.
- Requests are sent explicitly by the user. The UI has no auto-submission,
  conversation storage or browser persistence. The endpoint does not log or
  save prompt/response bodies. As elsewhere in QnapAssistant, users should
  restrict network access to the NAS management port.
- `max_tokens` is selectable (256, 512, 1024, 2048) and is validated on
  the server. Submitted request size is limited to 256 KiB and a single JSON
  object; oversized data must be summarized rather than silently truncated.
  The global model or context settings are **not modified** by this test.

This is a manual interactive diagnostic. It does not implement SelfCare-side
automatic weekly data collection, injection or clinical review.

### Live RAM availability monitoring during local LLM tests

QnapAssistant's **ローカルAI 入出力テスト** page now includes a RAM chart
and low-watermark monitor for the NAS. It reads Linux `MemAvailable`
(which includes potentially reclaimable filesystem cache, and is preferable
to `MemFree` for deciding whether the NAS has room to load another model).

- Press **計測開始 / 履歴をリセット** before **Load / Check** to capture
  the initial model-load memory spike. Press **計測停止** when finished.
- When pressing **回答を生成**, RAM monitoring starts **automatically**
  if manual monitoring is not already active. It samples throughout model
  preparation and inference and stops automatically afterward. If the user
  started monitoring manually, it continues after the answer until stopped.
- Displays **current available RAM, minimum RAM since monitoring began,
  change from the first sample, installed RAM, sample count**, and a
  time-ordered line chart (0–total RAM on the vertical axis).
- `GET /api/system/memory` is a small read-only endpoint using existing
  `/proc/meminfo` metrics. It responds with
  `total_bytes`, `available_bytes`, and `sampled_at_unix_ms`.
  No config, provider keys, model runtime or history storage is accessed.
- Polls around **once per second**, with at most one request in flight.
  Only the most recent **300 samples / five minutes** are retained in
  browser memory for the graph; the low-watermark is maintained since
  the start of monitoring. No time-series data is saved to the NAS drive
  or sent to an external service. The trace is discarded on page reload.
- Browser background throttling or slow network requests can introduce
  gaps; short peaks between samples may be missed. The measurements cover
  **system-wide** available RAM, not per-model RAM or precise inference
  allocations. Available RAM may rise as filesystem cache is reclaimed.

For Qwen3-8B Q4_K_M at 16K on a 16 GB RAM NAS, compare the minimum
`MemAvailable` during a real full-week prompt, not just at idle, before
considering larger context windows.
### Asynchronous local LLM jobs (v0.5.9)

Existing OpenAI-compatible `POST /v1/chat/completions` is unchanged.
Callers that need to distinguish model loading, inference and failure can
instead submit the same **non-streaming, text-only** payload to
`POST /api/llm/jobs`. This endpoint is **local-provider only**; it never
forwards data to OpenRouter. Its 202 response contains a cryptographically
random 192-bit `request_id`, `status_url` and `result_url`.

```sh
curl -X POST http://NAS:11435/api/llm/jobs \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"こんにちは"}],"max_tokens":256,"stream":false}'
# 202 {"request_id":"req_...","status":"queued",...}
curl http://NAS:11435/api/llm/jobs/req_...
curl http://NAS:11435/api/llm/jobs/req_.../result
curl -X DELETE http://NAS:11435/api/llm/jobs/req_...
```

The global, read-only **`GET /api/llm/state`** also reports model lifecycle
`unloaded`, `queued`, `loading_model`, `ready`, `generating`,
`unavailable` or `remote`, based on both active jobs and llama.cpp `/health`.
It exposes no request IDs or private prompts and distinguishes process-alive
from backend-ready.

Job states: `queued`, `loading_model`, `ready`, `generating`,
`completed`, `failed`, `timed_out`, `cancelled`. State returns
elapsed/stage seconds, configured model and context, process-alive flag,
system MemAvailable, sample timestamp and a stable failure code. The
process-alive flag does *not* mean model ready. Do not infer fabricated
percentages or remaining seconds.

Queue is restricted to **one active** and four awaiting jobs; excess
submissions return HTTP 429. Load timeout and generation timeout are
independent settings (defaults 600 and 1800 seconds; supported 30–3600).
Set them using **Model residency / auto-unload** in the Web UI, or:

```ini
LLM_LOAD_TIMEOUT_SECONDS=600
LLM_GENERATE_TIMEOUT_SECONDS=1800
LLM_AUTO_UNLOAD=1
LLM_IDLE_TIMEOUT_SECONDS=300
```

A request lease is acquired **before** model loading, retained until
generation completes, and the idle stop path synchronizes with lease
acquisition. The background worker is in memory, stores only bounded
requests and responses, and expires completed jobs after 15 minutes.
No prompt/answer/job history is written to NAS disk by this feature.
Requests must use local text messages, `stream=false`, and no tools;
max request is 256 KiB, max completion is 256 KiB, and `max_tokens`
is restricted to 1–2048. Browser cross-origin calls are rejected; protect
the NAS management port from untrusted clients. The opaque job ID is a
bearer capability for status/result/cancellation: never publish it or
include it in analytics/logs.

If QnapAssistant itself restarts, unfinished in-memory jobs are lost and
the client sees 404 on its old ID. Clients should distinguish this from
`loading_model`, and request user confirmation before re-submitting
health data; do not automatically create duplicates. A successful status
poll means the *management API* is reachable, not proof of actual model
progress. Browser/HTTP/network timeouts are separate from configured
local model deadlines.


### Privacy-preserving inference diagnostics (v0.5.10)

The async jobs API now requests llama.cpp's OpenAI-compatible SSE stream internally,
but still returns the original **non-streaming completion JSON** at
`GET /api/llm/jobs/{id}/result`. This allows partial text to be inspected
if the backend fails or a generation deadline expires.

```
GET /api/llm/jobs/{id}/diagnostics
GET /api/llm/jobs/{id}/diagnostics?include_output=1
```

The first returns **no generated content**, only: state, error code, elapsed
time, generation seconds, first-token milliseconds (-1 if none), seconds since
last text delta, observed delta chunk count (not an exact token count),
optional backend-reported prompt/completion tokens, output bytes, truncation,
model/context, finish reason, and whether private raw text remains in RAM.
The second returns the generated text (`raw_output`) only after an explicit
read. A 192-bit random job ID is a bearer capability for this endpoint; never
log, publish or embed it in telemetry. Keep port 11435 on a trusted LAN.

Normal `generation_timeout` is now classified as
`generation_first_token_timeout` or `generation_timeout_partial`, depending
on whether any content delta was received. This distinguishes cold/slow
prefill from slow generation (but neither alone proves an OOM event).
Backend non-SSE JSON is still accepted for compatibility.

Limits: 256 KiB output per job, at most five outstanding jobs, and 15-minute
RAM-only retention after terminal state. **No prompt, raw output, or full
request/response is persisted by this feature.** Copy the explicit diagnostics
result when investigating a failure, before the 15-minute retention expires.
NAS service restarts discard in-memory raw output. QnapSelfCare v0.3.54 can
persist the redacted metadata separately and retrieve raw only upon request.
