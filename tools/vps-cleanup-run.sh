#!/bin/sh
# Чистка: снять зависшие задания и удалить осиротевшие строки узлов.
# Живые узлы (в активных группах) не трогаем.
set -e

echo "=== снимаю зависшие задания ==="
runuser -u postgres -- psql -d compute_control -c "
UPDATE jobs
   SET state = 'failed',
       error = 'cancelled: left over from a failed group start',
       finished_at = now()
 WHERE state = 'queued'
RETURNING left(id::text, 8) AS job, model"

echo "=== удаляю осиротевшие строки узлов ==="

runuser -u postgres -- psql -d compute_control -c "
DELETE FROM worker_nodes w
 WHERE w.name IN ('DESKTOP-IOGJAOP', 'NOWUT')
   AND NOT EXISTS (
     SELECT 1 FROM topology_stages st
     JOIN topologies t ON t.id = st.topology_id
     WHERE st.worker_id = w.id AND t.status IN ('active', 'staging')
   )
RETURNING left(w.id::text, 8) AS id, w.name, to_char(w.last_seen_at, 'MM-DD HH24:MI') AS seen"

echo "=== что осталось ==="
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(w.id::text, 8) AS id, w.name,
       COALESCE(NULLIF(w.served_model, ''), '-') AS served,
       to_char(w.last_seen_at, 'MM-DD HH24:MI') AS seen
FROM worker_nodes w ORDER BY w.name"

runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT state, count(*) FROM jobs GROUP BY state ORDER BY state"
