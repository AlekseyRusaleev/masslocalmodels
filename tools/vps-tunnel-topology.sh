#!/bin/sh
# Готовит топологию к проверке туннеля: стадия owner-pc-cpu, слои 18–35.
TOPO=67ab59ea-74dd-414f-8720-5e956fde535a
STAGE=$(runuser -u postgres -- psql -d compute_control -tAc \
  "SELECT id FROM worker_nodes WHERE name = 'owner-pc-cpu'")
echo "stage worker: $STAGE"

runuser -u postgres -- psql -d compute_control -c "
DELETE FROM topology_stages WHERE topology_id = '$TOPO' AND stage_index = 1"

runuser -u postgres -- psql -d compute_control -c "
INSERT INTO topology_stages (topology_id, stage_index, worker_id, layer_start, layer_end, share, role)
VALUES ('$TOPO', 1, '$STAGE', 18, 35, 0.5, 'compute')"

runuser -u postgres -- psql -d compute_control -c "
UPDATE topologies SET status = 'active' WHERE id = '$TOPO'"

runuser -u postgres -- psql -d compute_control -c "
SELECT t.id, t.status, st.stage_index, w.name
FROM topologies t
JOIN topology_stages st ON st.topology_id = t.id
JOIN worker_nodes w ON w.id = st.worker_id
WHERE t.id = '$TOPO' ORDER BY st.stage_index"
