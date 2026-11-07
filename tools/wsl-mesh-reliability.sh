#!/usr/bin/env bash
# Надёжность публичной сети: несколько попыток, что реально приходит в ответе.
set -uo pipefail

base="http://127.0.0.1:9337/v1"

run_once() {
  local model="$1" index="$2"
  local out="/tmp/retry-$index.json"
  curl -s -m 240 "$base/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Назови столицу Франции одним словом.\"}],\"max_tokens\":30}" \
    -o "$out"

  python3 - "$model" "$index" "$out" <<'PY'
import json, sys
model, index, path = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    data = json.load(open(path))
except Exception:
    print(f'  попытка {index}: пустой ответ')
    raise SystemExit
if 'error' in data:
    message = str(data['error'].get('message'))[:90]
    print(f'  попытка {index}: ОШИБКА — {message}')
    raise SystemExit
served = data.get('model') or '?'
usage = data.get('usage') or {}
text = (data['choices'][0]['message'].get('content') or '').strip().replace('\n', ' ')
match = 'совпало' if served == model else 'ПОДМЕНА'
print(f'  попытка {index}: {match}, токенов {usage.get("completion_tokens")}, ответ: {text[:40]}')
PY
}

echo "=== 6 попыток к unsloth/Qwen3-8B-GGUF@main:Q4_K_M ==="
for i in 1 2 3 4 5 6; do
  run_once "unsloth/Qwen3-8B-GGUF@main:Q4_K_M" "$i"
done

echo
echo "=== 3 попытки к unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M ==="
for i in 7 8 9; do
  run_once "unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M" "$i"
done
