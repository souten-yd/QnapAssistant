# Resident model mode

QnapAssistant 0.3.3 keeps the default LLM/ASR/TTS models resident after automatic provisioning. The management UI/API can explicitly unload and reload LLM or voice services. Piper Plus uses its JSONL interactive mode so the Tsukuyomi model and OpenJTalk state are loaded/warmed once instead of once per TTS request.
