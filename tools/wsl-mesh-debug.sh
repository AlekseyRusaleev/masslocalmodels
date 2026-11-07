#!/usr/bin/env bash
# Разбор неудачного запуска mesh-llm: показать настоящую причину падения.
set -uo pipefail

log=/tmp/serve-debug.log

echo "=== строки, не относящиеся к служебному health ==="
grep -vE 'event_system_health' "$log" | tail -40

echo
echo "=== контекст вокруг FATAL ==="
line="$(grep -n 'FATAL' "$log" | tail -1 | cut -d: -f1 || true)"
if [ -n "${line:-}" ]; then
  start=$((line - 20))
  [ "$start" -lt 1 ] && start=1
  sed -n "${start},$((line + 2))p" "$log"
else
  echo "FATAL не найден"
fi

echo
echo "=== возможные логи native-рантайма ==="
find "$HOME" /tmp -maxdepth 6 \( -name '*native*log*' -o -name 'skippy*.log' \) -mmin -180 2>/dev/null | head -10

echo
echo "=== файлы, изменённые за последний час (кэш mesh) ==="
find "$HOME/.cache" "$HOME/.mesh-llm" "$HOME/.local" -mmin -90 -type f 2>/dev/null | grep -vE 'native-runtimes/.*/lib/' | head -20
