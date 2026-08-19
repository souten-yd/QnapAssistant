# Architecture

## Runtime

QnapAssistantは2層です。

1. `qnap-assistant`: 静的Go管理サーバー。QPKG有効中は常駐。
2. `llama-server`: 重い推論プロセス。必要時だけ起動し、idle timeout後に停止。

公開ポートは11435です。`llama-server` は `127.0.0.1:11436` のみにbindし、管理サーバーが `/v1/*` をproxyします。

## Idle unload

管理サーバーはactive request数と最終完了時刻を追跡します。リクエスト処理中はアンロードせず、`IDLE_TIMEOUT_SECONDS` を超えた場合のみllama-serverのprocess groupを停止してRAMを解放します。次回 `/v1/*` 要求時は再起動し、`/health` がreadyになるまで待ってから転送します。

## Persistence

- Default model: `/share/Public/Qwen3-0.6B-Q8_0.gguf`
- Model directory: `/share/Public`
- Config: `/share/Public/QnapAssistant/config.env`
- Manager log: `/share/Public/QnapAssistant/admin.log`
- llama-server log: `/share/Public/QnapAssistant/llama-server.log`

## Management API

組込みWeb UIは、status/system memory、設定、ログ、GGUF一覧・選択・URLダウンロード、LLM load/unload/restartを管理API経由で操作します。

## Build strategy

高コストなllama.cpp runtimeはcommit SHAとTS-253Be向けflagsをkeyにActions cacheします。cache missは検証済みbaseline QPKGから復元し、通常CIで同じllama.cppを再コンパイルしません。fresh buildは明示的なworkflow_dispatchでのみ許可します。Go管理サーバーだけは毎回数秒で静的buildし、QDK gzipでQPKG化します。
