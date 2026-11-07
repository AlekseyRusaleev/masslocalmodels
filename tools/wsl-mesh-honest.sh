#!/usr/bin/env bash
# Что реально отдаёт публичная сеть: сверяем запрошенную модель с моделью в ответе.
set -uo pipefail

base="http://127.0.0.1:9337/v1"

ask() {
  local model="$1"
  local out=/tmp/ask.json
  start=$(date +%s%N)
  curl -s -m 300 "$base/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Ответь одним словом: проверка\"}],\"max_tokens\":40}" \
    -o "$out"
  end=$(date +%s%N)
  wall=$(( (end - start) / 1000000 ))

  python3 - "$model" "$wall" "$out" <<'PY'
import json, sys
requested, wall_ms, path = sys.argv[1], int(sys.argv[2]), sys.argv[3]
try:
    data = json.load(open(path))
except Exception as error:
    print(f'  запрошено: {requested}')
    print('  ответ не JSON:', error)
    raise SystemExit
if 'error' in data:
    print(f'  запрошено: {requested}')
    print('  ОШИБКА:', json.dumps(data['error'], ensure_ascii=False)[:200])
    raise SystemExit
served = data.get('model')
usage = data.get('usage') or {}
text = (data['choices'][0]['message'].get('content') or '').strip().replace('\n', ' ')
print(f'  запрошено: {requested}')
print(f'  обслужено: {served}')
print(f'  совпало:   {str(served) == requested}')
print(f'  {wall_ms} мс, токенов {usage.get("completion_tokens")}, текст: {text[:60]}')
PY
}

echo "=== 1. модель, которую сеть анонсирует как 27B ==="
ask "unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M"

echo
echo "=== 2. модель, которую реально анонсирует узел ==="
ask "unsloth/Qwen3-8B-GGUF@main:Q4_K_M"
