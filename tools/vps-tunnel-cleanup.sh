#!/bin/sh
# Уборка после проверки туннеля: временные аккаунты, узлы, топология.
runuser -u postgres -- psql -d compute_control -c "
DELETE FROM users
 WHERE email LIKE 'probe-mesh%@nshno.tech' OR email LIKE 'node-%@network.local'
 RETURNING email"

runuser -u postgres -- psql -d compute_control -c "
UPDATE topologies SET status = 'retired'
 WHERE status IN ('active', 'draft', 'staging')
 RETURNING id, version, status"

runuser -u postgres -- psql -d compute_control -c "
SELECT name, COALESCE(NULLIF(served_model, ''), '-') AS served, state
FROM worker_nodes ORDER BY name"
