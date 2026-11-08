#!/bin/sh
# Состояние заданий qwen2.5: кто держит аренду, что в очереди. Только чтение.
runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT left(id::text, 8)            AS job,
       state,
       attempts,
       left(COALESCE(error, ''), 40) AS err,
       to_char(claimed_at, 'HH24:MI:SS')      AS claimed,
       to_char(lease_expires_at, 'HH24:MI:SS') AS lease
FROM jobs
WHERE model LIKE 'qwen2.5%'
ORDER BY created_at DESC
LIMIT 6"

runuser -u postgres -- psql -d compute_control -P pager=off -c "
SELECT count(*) FILTER (WHERE state = 'queued') AS queued,
       count(*) FILTER (WHERE state = 'claimed') AS claimed,
       count(*) FILTER (WHERE state = 'streaming') AS streaming
FROM jobs WHERE model LIKE 'qwen2.5%'"
