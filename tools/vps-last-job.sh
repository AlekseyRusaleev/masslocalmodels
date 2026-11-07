#!/usr/bin/env bash
# Показывает последнее задание: состояние, модель узла, digest, попытки.
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -tAc "
  SELECT state || ' | модель узла: ' || COALESCE(NULLIF(served_model, ''), 'не сообщена')
         || ' | digest: ' || COALESCE(NULLIF(left(served_digest, 16), ''), '—')
         || ' | попыток: ' || attempts
         || ' | токенов: ' || completion_tokens
  FROM jobs ORDER BY created_at DESC LIMIT 1"

echo "--- последние три задания ---"
psql "$DATABASE_URL" -tAc "
  SELECT created_at::time(0) || ' | ' || model || ' | ' || state || ' | ' ||
         COALESCE(NULLIF(served_model, ''), '—') || ' | ' || completion_tokens
  FROM jobs ORDER BY created_at DESC LIMIT 3"
