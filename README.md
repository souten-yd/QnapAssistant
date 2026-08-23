# QnapAssistant

QNAP NAS向けの軽量AI/音声アシスタントQPKGです。初期ターゲットは **QNAP TS-253Be / Intel Celeron J3455 / x86_64** です。

## 特徴

- QTS App Centerへ手動インストールできる `.qpkg`
- OpenAI互換API: `http://<NAS-IP>:11435/v1/`
- LLMを **Local Qwen/llama.cpp** と **OpenRouter** から選択可能
- OpenRouter primary model + ordered fallback models、provider routing、price ceiling、reasoning等を設定可能
- OpenRouter API Keyは通常設定とは分離し、0600のsecretファイルへ保存。GET API/UIへキー本体を返さない
- LLM / ASR / TTSごとに **auto-unload ON/OFF と idle timeoutを独立設定**
- Web設定UI: `http://<NAS-IP>:11435/`
- `/share/Public` にある別GGUFの選択・追加ダウンロード
- ローカル既定モデル: 公式 `Qwen3-0.6B-Q8_0.gguf`
- Qwen3 ThinkingはLocal Qwen専用設定として OFF / ON / passthroughを切替可能
- M5GO向け ASR → LLM → TTS と multipart逐次音声ストリーミング
- `session_id` による長期音声会話履歴
- GitHub ActionsでQPKGを自動生成（重いllama.cpp再ビルドは通常行わない）

## LLM provider

### Local Qwen / llama.cpp

```text
LLM_PROVIDER=local
MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf
LLM_AUTO_UNLOAD=0
LLM_IDLE_TIMEOUT_SECONDS=300
```

`LLM_AUTO_UNLOAD=0` は常駐、`1` は指定秒数のアイドル後にローカルllama.cppを停止します。生成中のstreamはactive requestとして保護され、途中でアンロードされません。

### OpenRouter

```text
LLM_PROVIDER=openrouter
OPENROUTER_MODEL=openrouter/free
OPENROUTER_FALLBACK_MODELS=
```

API Keyは `config.env` には書きません。Web UIまたは次のAPIで設定します。

```text
PUT    /api/openrouter/key
GET    /api/openrouter/key       # configured/fingerprintのみ
DELETE /api/openrouter/key
GET    /api/openrouter/models?free=1
POST   /api/openrouter/test      # 既定はopenrouter/free + 8 tokens
```

モデル一覧、複数モデルfallback、providerの price/throughput/latency sort、provider only/ignore、data collection、ZDR、price ceiling、temperature/top_p/reasoningなどをWeb UIから変更できます。

詳細: [`docs/LLM_PROVIDERS.md`](docs/LLM_PROVIDERS.md)

> OpenRouterを有料モデルで利用する場合、QnapAssistantの管理/OpenAI互換APIへ到達できるクライアントはAPI利用料金を発生させられます。現在のQnapAssistantにはアプリ独自の認証層がないため、信頼できるLAN/Tailscale等に公開範囲を限定してください。

## Voice Pipeline

M5GOなどのクライアントは録音・発話終了判定・表示・音声再生を担当し、QNAP側で **ASR → LLM → TTS** を処理します。LLMだけはLocal/OpenRouterを切り替えられます。

```text
M5GO utterance
   ↓
SenseVoiceSmall INT8 (ASR)
   ↓
Local Qwen3-0.6B  または  OpenRouter model(s)
   ↓
Piper Plus (primary TTS) / Supertonic 3 (fallback)
   ↓
small multipart WAV chunks
   ↓
M5GO ring-buffer playback
```

発話入力は固定秒数ではなく、PTT `listen.end` または将来のVAD終端までを1 utteranceとして扱います。LLM回答全体の長さと、M5GOへ送る8〜18文字程度のTTSチャンク長は独立しています。

主なAPI:

```text
GET  /api/voice/status
GET  /api/voice/protocol
POST /api/voice/start|stop|restart
POST /v1/audio/transcriptions
POST /v1/audio/speech
POST /v1/audio/speech/stream
POST /v1/voice/chat
POST /v1/voice/chat/stream
```

`system / optional max_tokens / history / session_id / reset_session` の仕様は [`docs/VOICE_CONTEXT.md`](docs/VOICE_CONTEXT.md) を参照してください。

## ASR / TTS auto-unload

```text
ASR_AUTO_UNLOAD=0
ASR_IDLE_TIMEOUT_SECONDS=300
TTS_AUTO_UNLOAD=0
TTS_IDLE_TIMEOUT_SECONDS=300
```

ASRを有効にした場合はSenseVoice recognizer/ONNX sessionだけを解放し、TTSを有効にした場合はresident Piperとロード済みSupertonic sessionだけを解放します。voice worker自体は必要に応じて維持され、次回要求で対象モデルのみlazy loadします。

詳細: [`docs/RESIDENT_MODE.md`](docs/RESIDENT_MODE.md)

## 保存先

```text
/share/Public/Qwen3-0.6B-Q8_0.gguf
/share/Public/QnapAssistant/config.env
/share/Public/QnapAssistant/openrouter.key     # mode 0600, APIでは値を返さない
/share/Public/QnapAssistant/qnapassistant.pid
/share/Public/QnapAssistant/admin.log
/share/Public/QnapAssistant/llama-server.log
/share/Public/QnapAssistant/benchmark.txt
/share/Public/QnapAssistant/voice/
/share/Public/QnapAssistant/voice/sessions/
```

## Qwen3 Thinking

`THINKING_MODE` は **Local Qwenのみ** に適用します。

- `off`: Qwen3 thinkingを無効化
- `on`: thinkingを有効化
- `passthrough`: クライアント/モデル既定

OpenRouterへ `/think` / `/no_think` や llama.cpp固有 `chat_template_kwargs` は転送しません。OpenRouter reasoningはOpenRouter専用設定で制御します。

## Voice reply length

```text
VOICE_REPLY_MAX_TOKENS=0
```

`0` は `max_tokens` を送らず、選択したLLM backendの標準値に従います。1〜2048を明示した場合のみ回答全体の上限として使います。M5GO音声チャンク長とは無関係です。

## 管理UI / Debug API

ブラウザ: `http://<NAS-IP>:11435/`

```text
GET  /health
GET  /api/status
GET/PUT /api/config
GET/PUT /api/thinking
GET  /api/logs
GET  /api/models
POST /api/models/select
POST /api/models/download
GET/PUT/DELETE /api/openrouter/key
GET  /api/openrouter/models
POST /api/openrouter/test
POST /api/llm/start|stop|restart
GET  /api/voice/status
POST /api/voice/start|stop|restart
```

## TS-253Be 実機ベースライン

QNAP TS-253Be / Celeron J3455:

- Qwen3-0.6B Q8_0: 約610 MiB
- `llama-bench pp128`: 16.41 ± 1.67 tok/s
- `llama-bench tg64`: 7.75 ± 1.39 tok/s
- Piper Plus resident TTS: 実測 RTF 約0.23〜0.29
- SenseVoice / Piper / Qwen local経路は実機確認済み

OpenRouter追加機能は0.5で導入し、ネットワーク/API Key実機疎通はNAS上の `POST /api/openrouter/test` を使って確認します。

## GitHub Actionsのコスト抑制

通常CIでは **llama.cppを毎回再ビルドしません**。pinned runtimeをActions cacheから復元し、cache miss時のみ成功済みbaseline QPKG artifactを再利用します。それも無い場合は高コストな再ビルドを自動開始せずfail-fastします。sherpa-onnxもstatic dependencyをキャッシュします。

同じPRの古いrunは `concurrency` とActions APIでキャンセルし、docs/READMEだけの変更ではQPKG CIを起動しません。

## QNAPへのインストール

1. `Build QNAP QPKG` の成功Artifact `QnapAssistant-x86_64` を取得
2. QTS → App Center → 手動インストール
3. Qnap Assistantを有効化
4. `http://<NAS-IP>:11435/` を開く
5. Local利用ならQwen、OpenRouter利用ならAPI Key/モデルを設定
6. Voice Pipeline利用時はASR/TTS assetsを準備

モデルをauto-unloadしても、GGUF/音声モデルのファイル自体はディスクに残ります。
