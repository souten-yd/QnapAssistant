# QnapAssistant

QNAP NAS向けのローカルAIアシスタントQPKGです。`llama.cpp` の `llama-server` をQNAP App Centerから起動し、Qwen3-0.6BをOpenAI互換APIとWeb UIで利用できます。

初期ターゲットは **QNAP TS-253Be (Intel Celeron J3455 / x86_64)** です。GitHub ActionsがTS-253Be互換のCPU命令セットで`llama-server`をビルドし、QDKで`.qpkg`を生成します。

## 主要仕様

- QPKG名: `QnapAssistant`
- Web UI / API: `http://<NAS-IP>:11435/`
- OpenAI互換API: `http://<NAS-IP>:11435/v1/`
- デフォルトモデル: `Qwen3-0.6B-Q8_0.gguf`
- **モデル保存先: `/share/Public/Qwen3-0.6B-Q8_0.gguf`**
- モデルは初回起動時だけ公式Qwen Hugging Faceからダウンロード
- QPKG更新・削除時もPublic共有上のモデルは保持
- デフォルト: 4 threads / ctx 4096 / batch 256 / parallel 1

## QPKGの作り方

GitHubの **Actions → Build QNAP QPKG → Run workflow** を実行します。成功後、`QnapAssistant-x86_64` artifactから`.qpkg`とSHA-256ファイルを取得できます。

`v*`タグをpushすると同じ成果物がGitHub Releaseにも添付されます。

## QNAPへのインストール

1. QTSのApp Centerを開く。
2. 手動インストールから生成された`.qpkg`を選ぶ。
3. Qnap Assistantを有効化する。
4. 初回起動時にQwen3-0.6Bが`/share/Public/Qwen3-0.6B-Q8_0.gguf`へダウンロードされる。
5. `http://<NAS-IP>:11435/`を開く。

初回ダウンロード中はWeb UIがまだ開けません。ログは `/share/Public/QnapAssistant/llama-server.log` に出力されます。

## 設定

`/share/Public/QnapAssistant/config.env` を編集できます。変更後はApp CenterからQnap Assistantを再起動してください。

主な項目:

```sh
MODEL_PATH=/share/Public/Qwen3-0.6B-Q8_0.gguf
HOST=0.0.0.0
PORT=11435
THREADS=4
CONTEXT=4096
BATCH=256
UBATCH=128
PARALLEL=1
```

別のGGUFを使う場合は`MODEL_PATH`と`MODEL_URL`を変更できます。

## TS-253Be向けビルド

J3455互換性を優先し、`GGML_NATIVE=OFF`、SSE4.2有効、AVX/AVX2/FMA/F16C/BMI2無効でビルドします。さらにAlpine/musl環境で静的リンクし、QTS側glibcバージョンへの依存を避けます。GitHub ActionsランナーのCPUに最適化されたバイナリを誤って生成しないためです。

llama.cppとQDKはGitコミットSHAで固定し、再現可能なビルドにしています。更新はworkflowとbuild script内のSHAを意図的に変更して行います。

## 動作確認

NAS上でSSHが利用できる場合:

```sh
/share/*/.qpkg/QnapAssistant/start-stop.sh status
curl http://127.0.0.1:11435/health
curl http://127.0.0.1:11435/v1/models
```

チャットAPI例:

```sh
curl http://127.0.0.1:11435/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"Qwen3-0.6B","messages":[{"role":"user","content":"こんにちは"}],"max_tokens":128}'
```

## ディレクトリ

```text
.github/workflows/build-qpkg.yml  GitHub Actions
qpkg.cfg                          QPKG metadata
package_routines                  install-time validation
shared/                           service/config/model downloader
x86_64/                           generated llama-server payload
scripts/build-llama.sh            TS-253Be compatible build
scripts/build-qpkg.sh             QDK package build
scripts/validate.sh               static validation
```

## 注意

初版はx86_64 QNAP専用です。QTS 5.0以降を対象とし、TS-253Beを主対象にしています。既定ポートは11435です。必要なら `/share/Public/QnapAssistant/config.env` の `PORT` を変更できます。
