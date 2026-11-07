#!/usr/bin/env bash
# Ставит зависимости для прототипа пайплайна и скачивает модель.
#
# hf_transfer нужен потому, что обычная загрузка Hugging Face идёт одним
# потоком (измерено ~1.6 MB/s), а параллельная — 10 MB/s.
set -u

VENV="$HOME/mesh-py"
PIP="$VENV/bin/pip"
PY="$VENV/bin/python"
MODEL="${1:-Qwen/Qwen3-0.6B}"

echo "=== зависимости ==="
"$PIP" install --quiet numpy transformers safetensors accelerate hf_transfer 2>&1 | tail -4
"$PY" - <<'PY'
import numpy, transformers, torch
print("numpy", numpy.__version__, "| transformers", transformers.__version__, "| torch", torch.__version__)
PY

echo "=== скачивание модели $MODEL ==="
export HF_HUB_ENABLE_HF_TRANSFER=1
"$PY" - "$MODEL" <<'PY'
import sys, time
from huggingface_hub import snapshot_download

model = sys.argv[1]
started = time.time()
path = snapshot_download(model, allow_patterns=["*.json", "*.safetensors", "*.txt", "*.model"])
elapsed = time.time() - started
print(f"скачано за {elapsed:.0f} с в {path}")
PY
du -sh "$HOME/.cache/huggingface" 2>/dev/null | tail -1
