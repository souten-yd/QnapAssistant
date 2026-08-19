# Architecture

## Runtime flow

1. QTS App Center invokes `start-stop.sh start`.
2. The service creates/loads persistent `/share/Public/QnapAssistant/config.env`.
3. If `/share/Public/Qwen3-0.6B-Q8_0.gguf` does not exist, `download-model.sh` fetches it atomically to a `.part` file and renames it after success.
4. `llama-server` starts on port 11435 with four CPU threads.
5. The built-in llama.cpp Web UI and OpenAI-compatible `/v1` API are exposed directly.
6. PID is stored inside the QPKG directory; logs are persistent under `/share/Public/QnapAssistant/`.

## Persistence

The model is deliberately outside the QPKG installation directory. Reinstalling or upgrading the QPKG therefore does not force a 639MB model download. Uninstalling QnapAssistant also leaves the model in Public so deletion remains an explicit user action.

## Build reproducibility

- llama.cpp is pinned to a commit SHA.
- QNAP QDK is pinned to a commit SHA.
- `GGML_NATIVE=OFF` prevents GitHub runner CPU capabilities leaking into the target binary.
- AVX-family features unavailable on the target are explicitly disabled.
- Action validation scans the emitted executable for AVX-family vector mnemonics before packaging.
- llama.cpp is built as a static musl executable in Alpine Linux, avoiding QTS glibc version coupling.
