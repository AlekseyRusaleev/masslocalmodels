#!/usr/bin/env bash
# Спайк Petals: изолированное окружение в WSL, проверка совместимости с CUDA и Petals.
set -u

VENV="$HOME/petals-spike"
log() { echo "=== $* ==="; }

log "venv"
if [ ! -d "$VENV" ]; then
  python3 -m venv "$VENV" || { echo "venv failed (нужен python3-venv)"; exit 1; }
fi
# shellcheck disable=SC1091
. "$VENV/bin/activate"
python -V

log "pip upgrade"
pip install --quiet --upgrade pip wheel setuptools 2>&1 | tail -3

log "torch (cu128, поддержка sm_120)"
pip install --quiet torch --index-url https://download.pytorch.org/whl/cu128 2>&1 | tail -5

log "проверка GPU в torch"
python - <<'PY'
import torch
print("torch", torch.__version__, "cuda", torch.version.cuda)
print("available", torch.cuda.is_available())
if torch.cuda.is_available():
    print("device", torch.cuda.get_device_name(0))
    print("capability", torch.cuda.get_device_capability(0))
    x = torch.randn(1024, 1024, device="cuda")
    print("matmul ok", float((x @ x).sum()) != 0.0)
PY

log "petals"
pip install --quiet petals 2>&1 | tail -15
python - <<'PY'
import petals, transformers
print("petals", getattr(petals, "__version__", "?"))
print("transformers", transformers.__version__)
from petals.models import DistributedBloomForCausalLM  # noqa: F401
print("petals.models import ok")
PY

log "какие архитектуры умеет Petals"
python - <<'PY'
import petals.models as m
print("models:", [n for n in dir(m) if n.startswith("Distributed")])
PY
