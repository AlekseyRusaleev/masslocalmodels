#!/bin/sh
# Инвентаризация перед чисткой: дубли узлов и их участие в активных группах.
echo "=== строки узлов ==="
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(w.id::text, 8) AS id, w.name,
       COALESCE(NULLIF(w.served_model, ''), '-') AS served,
       to_char(w.last_seen_at, 'MM-DD HH24:MI') AS seen,
       (SELECT count(*) FROM topology_stages st
          JOIN topologies t ON t.id = st.topology_id
         WHERE st.worker_id = w.id AND t.status IN ('active','staging')) AS in_groups
FROM worker_nodes w ORDER BY w.name, w.last_seen_at DESC NULLS LAST"

echo "=== кто в активных группах ==="
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(t.id::text, 8) AS topo, st.stage_index AS idx,
       w.name, left(w.id::text, 8) AS worker
FROM topologies t
JOIN topology_stages st ON st.topology_id = t.id
JOIN worker_nodes w ON w.id = st.worker_id
WHERE t.status IN ('active','staging')
ORDER BY t.created_at DESC, st.stage_index"

echo "=== задания в очереди ==="
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(id::text, 8) AS job, state, model, to_char(created_at, 'HH24:MI') AS at
FROM jobs WHERE state = 'queued' ORDER BY created_at"
