#!/usr/bin/env bash
# Запуск прототипа пайплайна: стадия (блоки со split) и координатор (до split).
set -u

PY="$HOME/mesh-py/bin/python"
SCRIPT="$HOME/spike/pipeline_demo.py"
MODEL="${MODEL:-Qwen/Qwen3-0.6B}"
SPLIT="${SPLIT:-14}"
TOKENS="${TOKENS:-32}"
PORT="${PORT:-31330}"

echo "=== стадия: блоки с $SPLIT ==="
"$PY" "$SCRIPT" second --model "$MODEL" --split "$SPLIT" --port "$PORT" > /tmp/second.log 2>&1 &
SECOND_PID=$!

for _ in $(seq 1 120); do
  if grep -q "слушаю порт" /tmp/second.log 2>/dev/null; then break; fi
  if ! kill -0 "$SECOND_PID" 2>/dev/null; then
    echo "стадия упала:"; tail -20 /tmp/second.log; exit 1
  fi
  sleep 1
done
tail -3 /tmp/second.log

echo "=== координатор: блоки до $SPLIT ==="
"$PY" "$SCRIPT" first --model "$MODEL" --split "$SPLIT" \
  --peer "127.0.0.1:$PORT" --max-new-tokens "$TOKENS" 2>&1 | tail -8
RC=$?

kill "$SECOND_PID" 2>/dev/null
wait "$SECOND_PID" 2>/dev/null
echo "=== лог стадии ==="
tail -5 /tmp/second.log
exit $RC
