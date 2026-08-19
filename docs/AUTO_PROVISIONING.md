# QnapAssistant 0.3.2 automatic provisioning

Version 0.3.2 removes the manual model-install steps from normal operation.

On QPKG start, the manager asynchronously provisions any missing default assets in this order:

1. Qwen3-0.6B Q8_0 GGUF
2. SenseVoiceSmall INT8 ASR
3. Supertonic 3 INT8 fallback TTS
4. Piper Plus v1.13.0 runtime
5. Tsukuyomi-chan multilingual ONNX + config
6. OpenJTalk UTF-8 1.11 dictionary

The management API remains available while provisioning. `GET /api/bootstrap` reports readiness and progress. Voice API requests also call the provisioning gate so a missing voice asset is fetched automatically instead of requiring a manual download command.

Piper Plus is the default TTS backend in the worker; Supertonic remains the fallback/comparison backend.

The OpenJTalk dictionary is downloaded by the resident manager, SHA-256 verified (`fe6ba0e43542cef98339abdffd903e062008ea170b04e7e2a35da805902f382a`), safely extracted under `/share/Public/QnapAssistant/voice/openjtalk`, and passed to Piper through `OPENJTALK_DICTIONARY_PATH`. Piper network-side dictionary auto-download is disabled at inference time.

Physical findings leading to this change:

- The isolated QPKG glibc/libstdc++ loader successfully starts Piper Plus v1.13.0 on TS-253Be/QTS.
- Direct Japanese synthesis reached model load/warmup but produced a 44-byte empty WAV because `OpenJTalk is not available`.
- Therefore the remaining blocker is Japanese G2P dictionary provisioning, not GLIBC, ONNX Runtime, or WAV parsing.
