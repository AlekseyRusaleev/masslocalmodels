#!/bin/sh
# Токены и учёт последних заданий — read-only.
runuser -u postgres -- psql -d compute_control -c "
SELECT to_char(created_at,'HH24:MI:SS') AS at,
       left(id::text,8) AS job,
       state,
       prompt_tokens, completion_tokens, eval_ms,
       left(error, 40) AS err
FROM jobs
ORDER BY created_at DESC
LIMIT 5"

runuser -u postgres -- psql -d compute_control -c "\d usage_ledger"
