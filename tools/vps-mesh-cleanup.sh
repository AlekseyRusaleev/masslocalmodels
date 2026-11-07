#!/bin/sh
# Уборка после проверки шард-режима: тестовые аккаунты, топология, состояние узлов.
runuser -u postgres -- psql -d compute_control -c "
DELETE FROM users
 WHERE email IN ('node-mjwqsvkgyt5b@network.local', 'probe-mesh@nshno.tech')
 RETURNING email"

runuser -u postgres -- psql -d compute_control -c "
UPDATE topologies SET status = 'retired'
 WHERE status = 'active'
 RETURNING id, version, status"

runuser -u postgres -- psql -d compute_control -c "
SELECT name,
       COALESCE(NULLIF(served_model, ''), '-') AS served,
       COALESCE(NULLIF(mesh_rpc_addr, ''), '-') AS rpc,
       state
FROM worker_nodes
ORDER BY name"

runuser -u postgres -- psql -d compute_control -c "
SELECT count(*) AS admin_users FROM users WHERE email = 'probe-mesh@nshno.tech'"
