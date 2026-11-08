#!/usr/bin/env bash
# Что платформа знает об участниках: версия программы, состояние, модель, RTT.
set -euo pipefail

set -a
# shellcheck disable=SC1091
. /etc/compute-control/api-gateway.env
set +a

PSQL=(psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -P pager=off)

echo "--- участники ---"
"${PSQL[@]}" -c "
SELECT left(w.id::text,8) AS worker,
       w.hostname,
       COALESCE(w.agent_version,'-') AS ver,
       w.state,
       COALESCE(w.served_model,'-') AS model,
       COALESCE(w.rtt_ms,0) AS rtt_ms,
       to_char(w.last_seen_at,'DD.MM HH24:MI:SS') AS last_seen
  FROM worker_nodes w
 ORDER BY w.last_seen_at DESC NULLS LAST
 LIMIT 12;"

echo "--- релизный фид ---"
curl -sS --max-time 15 https://api.nshno.tech/node/release.json; echo
