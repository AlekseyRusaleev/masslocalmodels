#!/usr/bin/env bash
# Три последовательных запроса к mesh-llm: скорость после прогрева.
set -uo pipefail

model="local-gguf/sha256-17b032c4784505a6"
base="http://127.0.0.1:9337/v1"
prompt='Перечисли десять коротких правил распределённых систем, по одному предложению каждое.'

for i in 1 2 3; do
  out=/tmp/warm$i.json
  start=$(date +%s%N)
  curl -s -m 300 "$base/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"$prompt\"}],\"max_tokens\":300}" \
    -o "$out"
  end=$(date +%s%N)
  wall_ms=$(( (end - start) / 1000000 ))

  python3 - "$out" "$wall_ms" "$i" <<'PY'
import json, sys
path, wall_ms, index = sys.argv[1], int(sys.argv[2]), sys.argv[3]
data = json.load(open(path))
usage = data.get('usage') or {}
tokens = usage.get('completion_tokens') or 0
speed = tokens / (wall_ms / 1000) if wall_ms else 0
print(f"  запрос {index}: {tokens} токенов за {wall_ms} мс → {speed:.1f} tok/s (с учётом префила)")
PY
done
