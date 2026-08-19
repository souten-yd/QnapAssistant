# Voice latency tuning for 0.3.4

Physical TS-253Be results with 0.3.3 resident Piper showed Piper Plus/Tsukuyomi at roughly 0.23-0.29 RTF and about 0.7-0.9 s for a ~3 s utterance. Three resident end-to-end voice-chat runs completed in 8.88 s, 16.37 s and 12.32 s. The LLM stage dominated the latency at 6.06-11.34 s.

0.3.4 keeps the resident architecture and changes the voice-chat generation policy rather than replacing models:

- use llama.cpp `chat_template_kwargs.enable_thinking=false` plus `reasoning_effort=none` for Qwen thinking-off mode;
- keep a stable short Japanese system prompt so prompt caching can be reused;
- default voice replies to 48 output tokens and temperature 0.2;
- include llama.cpp prompt/generation timings in `/v1/voice/chat` diagnostics;
- preload SenseVoice and Piper/Tsukuyomi, while keeping Supertonic downloaded but loading it lazily only if fallback is actually needed.

The next physical gate is repeated `/v1/voice/chat` measurement. If stable full-response latency is acceptable, the following milestone moves to sentence/chunk streaming so playback can begin before the full LLM reply is complete.
