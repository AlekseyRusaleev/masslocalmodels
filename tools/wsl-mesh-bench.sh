#!/usr/bin/env bash
# Замер скорости mesh-llm на локальной машине: обычный и потоковый ответ.
set -uo pipefail

model="local-gguf/sha256-17b032c4784505a6"
base="http://127.0.0.1:9337/v1"
prompt='Перечисли пять коротких правил распределённых систем, по одному предложению каждое.'

echo "=== модель в API ==="
curl -s -m 10 "$base/models" | head -c 200; echo

echo
echo "=== 1. обычный ответ (max_tokens 300) ==="
curl -s -m 300 "$base/chat/completions" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"$prompt\"}],\"max_tokens\":300}" \
  -o /tmp/resp1.json -w '  http=%{http_code}  время=%{time_total}s\n'

python3 - <<'PY'
import json
data = json.load(open('/tmp/resp1.json'))
usage = data.get('usage') or {}
choice = data['choices'][0]
print('  usage:', usage)
print('  finish:', choice.get('finish_reason'))
text = (choice['message'].get('content') or '').replace('\n', ' ')
print('  текст:', text[:180])
completion = usage.get('completion_tokens') or 0
if completion:
    print('  токенов в ответе:', completion)
PY

echo
echo "=== 2. потоковый ответ (первый токен и полное время) ==="
start=$(date +%s%N)
first=""
curl -s -N -m 300 "$base/chat/completions" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$model\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Скажи одно слово: проверка\"}],\"max_tokens\":80}" \
  -o /tmp/resp2.sse
end=$(date +%s%N)
events=$(grep -c '^data: ' /tmp/resp2.sse || true)
echo "  событий: $events, всего: $(( (end - start) / 1000000 )) мс"
grep '^data: ' /tmp/resp2.sse | head -2 | cut -c1-160
grep '^data: ' /tmp/resp2.sse | tail -1 | cut -c1-160

echo
echo "=== 3. VRAM после загрузки ==="
nvidia-smi --query-gpu=memory.used,memory.total,temperature.gpu --format=csv,noheader
