#!/bin/sh
# Состояние шард-групп: кто какие слои держит и что объявил. Только чтение.
runuser -u postgres -- psql -d compute_control -c "
SELECT name,
       COALESCE(NULLIF(mesh_rpc_addr, ''), '-') AS rpc_addr,
       COALESCE(NULLIF(served_model, ''), '-') AS served,
       state
FROM worker_nodes
ORDER BY name"

runuser -u postgres -- psql -d compute_control -c "
SELECT t.id,
       t.version,
       t.status,
       m.name AS model,
       st.stage_index,
       w.name AS worker,
       st.layer_start,
       st.layer_end,
       COALESCE(NULLIF(w.mesh_rpc_addr, ''), '-') AS rpc_addr
FROM topologies t
JOIN model_revisions r ON r.id = t.revision_id
JOIN model_catalog m ON m.id = r.model_id
JOIN topology_stages st ON st.topology_id = t.id
JOIN worker_nodes w ON w.id = st.worker_id
WHERE t.status IN ('active', 'staging')
ORDER BY t.created_at DESC, st.stage_index
LIMIT 10"
