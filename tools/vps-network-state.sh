#!/bin/sh
# Состояние сети: узлы, активные группы слоёв, что каждый объявил, последние задания.
# Только чтение.
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT name,
       state,
       COALESCE(NULLIF(served_model, ''), '-') AS served,
       COALESCE(NULLIF(mesh_rpc_addr, ''), '-') AS rpc,
       to_char(last_seen_at, 'HH24:MI:SS') AS seen
FROM worker_nodes ORDER BY name"

runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT t.id, t.status, t.auto, m.name AS model, st.stage_index AS idx,
       w.name, st.layer_start, st.layer_end, st.role
FROM topologies t
JOIN model_revisions r ON r.id = t.revision_id
JOIN model_catalog m ON m.id = r.model_id
JOIN topology_stages st ON st.topology_id = t.id
JOIN worker_nodes w ON w.id = st.worker_id
WHERE t.status IN ('active', 'staging')
ORDER BY t.created_at DESC, st.stage_index"

runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT ws.name,
       COALESCE(h.meta_json->>'mesh_model', '-') AS mesh_model,
       COALESCE(h.meta_json->>'mesh_capable', '-') AS capable,
       COALESCE(h.meta_json->>'model_fits', '-') AS fits,
       to_char(h.observed_at, 'HH24:MI:SS') AS at
FROM (SELECT DISTINCT ON (worker_id) worker_id, meta_json, observed_at
      FROM worker_heartbeats
      ORDER BY worker_id, observed_at DESC) h
JOIN worker_nodes ws ON ws.id = h.worker_id
ORDER BY ws.name"

runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(id::text, 8) AS job, state, model,
       left(COALESCE(error, ''), 90) AS err,
       to_char(created_at, 'HH24:MI:SS') AS at
FROM jobs ORDER BY created_at DESC LIMIT 8"
