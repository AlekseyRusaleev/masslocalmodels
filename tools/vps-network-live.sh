#!/usr/bin/env bash
# Живость сети: когда последний heartbeat, какие задания, какие топологии.
set -euo pipefail

set -a
# shellcheck disable=SC1091
. /etc/compute-control/api-gateway.env
set +a

PSQL=(psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -P pager=off)

echo "--- время сервера и последние heartbeat ---"
"${PSQL[@]}" -c "
SELECT now() AS server_now,
       (SELECT max(observed_at) FROM worker_heartbeats) AS last_heartbeat,
       (SELECT count(*) FROM worker_heartbeats WHERE observed_at > now() - interval '10 minutes') AS hb_10min,
       (SELECT count(*) FROM worker_heartbeats WHERE observed_at > now() - interval '2 hours') AS hb_2h;"

echo "--- задания за сутки ---"
"${PSQL[@]}" -c "
SELECT state, count(*) AS n, to_char(max(created_at),'DD.MM HH24:MI') AS last
  FROM jobs
 WHERE created_at > now() - interval '24 hours'
 GROUP BY state ORDER BY n DESC;"

echo "--- топологии ---"
"${PSQL[@]}" -c "
SELECT left(id::text,8) AS id, COALESCE(model,'-') AS model, status, auto,
       to_char(created_at,'DD.MM HH24:MI') AS created,
       left(COALESCE(error,''), 70) AS error
  FROM topologies
 ORDER BY created_at DESC LIMIT 8;"

echo "--- релизный фид ---"
curl -sS --max-time 15 https://api.nshno.tech/node/release.json; echo
