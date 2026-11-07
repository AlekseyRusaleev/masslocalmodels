#!/usr/bin/env bash
# Замер публичной сети: список моделей и три запроса на 300 токенов.
set -uo pipefail

base="http://127.0.0.1:9337/v1"
parser="/mnt/c/Users/Admin/Desktop/masslocalmodels/tools/mesh_parse.py"
model="unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M"
prompt='Перечисли десять коротких правил распределённых систем, по одному предложению каждое.'

echo "=== доступные модели ==="
curl -s -m 15 "$base/models" | python3 "$parser" models | head -40

echo
echo "=== три запроса к $model, max_tokens=300 ==="
for i in 1 2 3; do
  start=$(date +%s%N)
  curl -s -m 600 "$base/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"$prompt\"}],\"max_tokens\":300}" \
    -o "/tmp/pub$i.json"
  end=$(date +%s%N)
  wall_ms=$(( (end - start) / 1000000 ))
  tokens=$(python3 -c "import json;d=json.load(open('/tmp/pub$i.json'));print((d.get('usage') or {}).get('completion_tokens') or 0)")
  speed=$(python3 -c "print(round($tokens/($wall_ms/1000),1) if $wall_ms else 0)")
  echo "  запрос $i: $tokens токенов за $wall_ms мс → $speed tok/s (сеть + префил + генерация)"
  python3 "$parser" usage < "/tmp/pub$i.json"
done
