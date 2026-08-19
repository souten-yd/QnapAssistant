# QnapAssistant

QNAP NAS向けの軽量ローカルAIアシスタントQPKGです。初期ターゲットは **QNAP TS-253Be / Intel Celeron J3455 / x86_64** です。

## 特徴

- QTS App Centerへ手動インストールできる `.qpkg`
- OpenAI互換API: `http://<NAS-IP>:11435/v1/`
- 軽量な管理APIだけを常駐し、**LLMはオンデマンドでロード**
- APIアクセスが無い状態が既定5分続くと `llama-server` を停止しモデルRAMを解放
- Web設定UI: `http://<NAS-IP>:11435/`
- llama.cppログ / 管理サーバーログをWeb/APIから取得
- threads / context / batch / ubatch / parallel / idle timeout /追加引数を変更可能
- `/share/Public` にある別の `.gguf` を選択可能
- URLから追加GGUFを `/share/Public` へダウンロード可能
- デフォルトモデル: 公式 `Qwen3-0.6B-Q8_0.gguf`
- Qwen3 Thinkingは既定OFF。UI/APIからOFF / ON / passthroughを切替可能
- GitHub ActionsでQPKGを自動生成

## Voice Pipeline（開発中）

M5GOなどのクライアントは録音・簡易VAD/ボタン・音声再生だけを担当し、QNAP側で **ASR → LLM → TTS** を一括処理します。

```text
M5GO audio
   ↓
SenseVoiceSmall INT8 (ASR, ja)
   ↓
Qwen3-0.6B (LLM)
   ↓
Supertonic 3 INT8 (TTS, ja)
   ↓
M5GO speaker
```

音声モデルはQPKGに同梱せず `/share/Public/QnapAssistant/voice` に保存します。通常NASユーザーからモデル保存先へ直接書き込ませず、QTSの管理サービス経由で取得するのが既定です。

```text
POST /api/voice/models/download   # 非同期で公式ASR/TTSモデルを取得
GET  /api/voice/models/download   # 進捗・完了状態
GET  /api/voice/status
POST /api/voice/start|stop|restart
POST /v1/audio/transcriptions
POST /v1/audio/speech
POST /v1/voice/chat
```

`POST /api/voice/models/download` はQTSで `admin` として動く管理サーバーが実行するため、SSHユーザーが `/share/Public/QnapAssistant` に書き込めなくても利用できます。`.tar.bz2` の展開もGo標準ライブラリで処理し、QTS側の `tar -j` / `bzip2` の有無に依存しません。

詳細設計・ベンチ基準は [`docs/VOICE_PIPELINE.md`](docs/VOICE_PIPELINE.md) を参照してください。

## 保存先

デフォルトモデル: `/share/Public/Qwen3-0.6B-Q8_0.gguf`

設定・PID・ログ:

```text
/share/Public/QnapAssistant/config.env
/share/Public/QnapAssistant/qnapassistant.pid
/share/Public/QnapAssistant/admin.log
/share/Public/QnapAssistant/llama-server.log
/share/Public/QnapAssistant/benchmark.txt
/share/Public/QnapAssistant/voice/
```

## オンデマンドロード

QNAP起動時に常駐するのは `qnap-assistant` 管理サーバーです。`:11435/v1/*` に要求が来るとlocalhostの `llama-server` を起動し、モデル準備完了後にAPIを転送します。`IDLE_TIMEOUT_SECONDS=300` の既定値では、active requestが無い状態が5分続くとLLMをアンロードします。`0`で自動アンロード無効です。

## Qwen3 Thinking

`THINKING_MODE=off` が既定です。Qwen3の `/v1/chat/completions` に対して、明示的な `/think` / `/no_think` が無い場合だけ `/no_think` を自動付与します。

- `off`: `/no_think` を自動付与
- `on`: `/think` を自動付与
- `passthrough`: クライアント要求を変更しない

非Qwen3モデルにはThinking指示を自動付与しません。

## 管理UI / Debug API

ブラウザ: `http://<NAS-IP>:11435/`

```text
GET  /health
GET  /api/status
GET  /api/config
PUT  /api/config
GET  /api/thinking
PUT  /api/thinking
GET  /api/logs?target=llama&lines=400
GET  /api/logs?target=admin&lines=400
GET  /api/models
POST /api/models/select
POST /api/models/download
POST /api/llm/start
POST /api/llm/stop
POST /api/llm/restart
GET  /api/voice/status
POST /api/voice/start
POST /api/voice/stop
POST /api/voice/restart
GET  /api/voice/models/download
POST /api/voice/models/download
```

## TS-253Be 実機結果

QNAP TS-253Be / Celeron J3455で確認済みです。

- Qwen3-0.6B Q8_0: 約610 MiB
- `llama-bench pp128`: 16.41 ± 1.67 tok/s
- `llama-bench tg64`: 7.75 ± 1.39 tok/s
- `/no_think` 実チャット: 約8.45 tok/s
- Thinking OFF自動適用: 実機確認済み
- 5分アイドル後のLLMアンロード: 実機確認済み
- voice worker起動: 実機確認済み
- ASR/TTS速度: 実機評価中

## QTSサービス互換性

QTS/BusyBox環境での実機検証を反映しています。

- `nohup` に依存しない
- 管理デーモンはSIGHUPを無視
- PIDファイルが無い/古い場合は `ps` から管理プロセスを再検出
- `status` の存在確認では `kill -0` を使わないため、通常NASユーザーからadmin所有のQTSサービス状態を確認可能
- `stop` / `restart` は実際にsignal権限が必要で、権限不足時は明示エラー
- 音声モデル取得は管理APIで実行し、通常ユーザーのファイル書き込み権限を要求しない

## 主な設定

```sh
MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf
MODEL_DIR=/share/Public
ADMIN_PORT=11435
BACKEND_PORT=11436
THREADS=4
THREADS_BATCH=4
CONTEXT=4096
BATCH=256
UBATCH=128
PARALLEL=1
THINKING_MODE=off
IDLE_TIMEOUT_SECONDS=300
EXTRA_ARGS=
VOICE_PORT=11437
VOICE_DIR=/share/Public/QnapAssistant/voice
ASR_MODEL_DIR=/share/Public/QnapAssistant/voice/sensevoice
TTS_MODEL_DIR=/share/Public/QnapAssistant/voice/supertonic3
ASR_LANGUAGE=ja
TTS_LANGUAGE=ja
ASR_THREADS=4
TTS_THREADS=2
TTS_STEPS=8
TTS_SPEED=1.0
```

## GitHub Actionsのコスト抑制

通常CIでは **llama.cppを毎回再ビルドしません**。pinned runtimeをActions cacheから復元し、cache miss時のみ既に成功したbaseline QPKG artifactから再利用します。それも無い場合は高コストな再ビルドを勝手に開始せずfail-fastします。sherpa-onnxも公式static-libをキャッシュし、毎回ソースビルドしません。

同じPRの古いrunは `concurrency` とActions APIでキャンセルし、docs/READMEだけの変更ではQPKG CIを起動しません。

## QNAPへのインストール

1. `Build QNAP QPKG` の成功Artifact `QnapAssistant-x86_64` を開く
2. `.qpkg` をQTS → App Center → 手動インストール
3. Qnap Assistantを有効化
4. `http://<NAS-IP>:11435/`を開く
5. 初回LLM要求時にQwen3-0.6Bが `/share/Public` へダウンロードされる
6. Voice Pipeline利用時は `POST /api/voice/models/download` でASR/TTSモデルを取得する

初回取得後はアイドルアンロードしてもGGUF/音声モデルはディスクに残ります。
