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
- GitHub ActionsでQPKGを自動生成

## 保存先

デフォルトモデル: `/share/Public/Qwen3-0.6B-Q8_0.gguf`

設定・ログ:

```text
/share/Public/QnapAssistant/config.env
/share/Public/QnapAssistant/admin.log
/share/Public/QnapAssistant/llama-server.log
/share/Public/QnapAssistant/benchmark.txt
```

## オンデマンドロード

QNAP起動時に常駐するのは `qnap-assistant` 管理サーバーです。`:11435/v1/*` に要求が来るとlocalhostの `llama-server` を起動し、モデル準備完了後にAPIを転送します。`IDLE_TIMEOUT_SECONDS=300` の既定値では、active requestが無い状態が5分続くとLLMをアンロードします。`0`で自動アンロード無効です。

## 管理UI / Debug API

ブラウザ: `http://<NAS-IP>:11435/`

```text
GET  /health
GET  /api/status
GET  /api/config
PUT  /api/config
GET  /api/logs?target=llama&lines=400
GET  /api/logs?target=admin&lines=400
GET  /api/models
POST /api/models/select
POST /api/models/download
POST /api/llm/start
POST /api/llm/stop
POST /api/llm/restart
```

`/api/status` ではLLMロード状態、active request数、RAM空き、load average、現在モデルなども確認できます。

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
IDLE_TIMEOUT_SECONDS=300
EXTRA_ARGS=
```

設定保存時は現在のLLMをアンロードし、次のAPI要求から新設定でロードします。

## GitHub Actionsのコスト抑制

通常CIでは **llama.cppを毎回再ビルドしません**。pinned runtimeをActions cacheから復元し、cache miss時のみ既に成功したbaseline QPKG artifactから再利用します。それも無い場合は高コストな再ビルドを勝手に開始せずfail-fastします。llama.cppを本当に更新するときだけ `workflow_dispatch` の `allow_llama_rebuild=true` を指定します。

同じPRの古いrunは `concurrency` とActions APIでキャンセルし、docs/READMEだけの変更ではQPKG CIを起動しません。QPKG圧縮もxzからgzipへ変更してパッケージ生成時間を短縮しています。

## QNAPへのインストール

1. `Build QNAP QPKG` の成功Artifact `QnapAssistant-x86_64` を開く
2. `.qpkg` をQTS → App Center → 手動インストール
3. Qnap Assistantを有効化
4. `http://<NAS-IP>:11435/`を開く
5. 初回LLM要求時にQwen3-0.6Bが `/share/Public` へダウンロードされる

初回取得後はアイドルアンロードしてもGGUFはディスクに残り、次回はRAMへの再ロードだけです。
