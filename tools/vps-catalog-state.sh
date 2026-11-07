#!/usr/bin/env bash
# Показывает каталог ревизий и текущее состояние учёта.
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

echo "--- каталог ---"
psql "$DATABASE_URL" -tAc "
  SELECT m.name || ' | ревизия ' || left(r.revision, 12) || ' | ' || r.status || ' | ' || r.quantization
         || ' | файлов: ' || (SELECT count(*) FROM model_artifacts a WHERE a.revision_id = r.id)
  FROM model_revisions r JOIN model_catalog m ON m.id = r.model_id
  ORDER BY r.created_at"

echo "--- digest'ы узлов ---"
psql "$DATABASE_URL" -tAc "
  SELECT w.name || ' | ' || d.model || ' | ' || left(d.digest, 16) FROM worker_model_digests d
  JOIN worker_nodes w ON w.id = d.worker_id ORDER BY w.name, d.model"

echo "--- задания с проверкой весов ---"
psql "$DATABASE_URL" -tAc "
  SELECT 'проверено: ' || count(*) FILTER (WHERE digest_verified) || ' из ' || count(*)
  FROM jobs WHERE state = 'done'"
