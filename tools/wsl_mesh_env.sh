#!/usr/bin/env bash
# Окружение для своего пайплайна слоёв: venv + torch (CUDA), проверка sm_120.
#
# Почему не pip по умолчанию: download.pytorch.org на этом канале отдаёт
# ~100 KB/s, колесо torch весит гигабайты. Пробуем зеркало и системный индекс,
# берём то, что быстрее; выбор печатается, чтобы не гадать, что установилось.
set -u

VENV="$HOME/mesh-py"
PIP="$VENV/bin/pip"
PY="$VENV/bin/python"
log() { echo "=== $* ==="; }

if [ ! -d "$VENV" ]; then
  python3 -m venv "$VENV" || { echo "venv не создался"; exit 1; }
fi
"$PIP" install --quiet --upgrade pip wheel setuptools 2>&1 | tail -2

log "какие колеса torch есть на зеркале"
timeout 30 curl -s https://mirrors.aliyun.com/pytorch-wheels/cu128/ \
  | grep -oE 'torch-[0-9]+\.[0-9]+\.[0-9]+[^"]*cp312[^"]*manylinux[^"]*' \
  | sort -u | tail -3

log "ставим torch (зеркало aliyun, затем PyPI)"
if ! "$PIP" install torch --find-links https://mirrors.aliyun.com/pytorch-wheels/cu128/ \
      --index-url https://pypi.org/simple 2>&1 | tail -6; then
  echo "зеркало не сработало, пробуем обычный индекс"
  "$PIP" install torch --index-url https://download.pytorch.org/whl/cu128 2>&1 | tail -6
fi

log "проверка GPU и sm_120"
"$PY" - <<'PY'
import torch
print("torch", torch.__version__, "cuda", torch.version.cuda)
print("available", torch.cuda.is_available())
if torch.cuda.is_available():
    name = torch.cuda.get_device_name(0)
    cap = torch.cuda.get_device_capability(0)
    print("device", name, "capability", cap)
    x = torch.randn(2048, 2048, device="cuda", dtype=torch.bfloat16)
    y = x @ x
    torch.cuda.synchronize()
    print("bf16 matmul ok", float(y.abs().sum()) > 0)
else:
    print("GPU не виден — дальше проверять нечего")
PY
