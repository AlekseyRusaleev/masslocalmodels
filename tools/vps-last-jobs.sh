#!/bin/sh
# Последние задания с аттестацией — read-only проверка платформы.
# Запуск: python tools/ssh_put.py tools/vps-last-jobs.sh /tmp/last-jobs.sh \
#         && python tools/ssh_run.py "bash /tmp/last-jobs.sh"
runuser -u postgres -- psql -d compute_control -c "
SELECT to_char(created_at,'HH24:MI:SS') AS at,
       left(id::text,8) AS job,
       state,
       served_model,
       left(served_digest,10) AS digest,
       digest_verified AS ver,
       attestation
FROM jobs
ORDER BY created_at DESC
LIMIT 6"

runuser -u postgres -- psql -d compute_control -c "
SELECT to_char(created_at,'HH24:MI') AS at, job_id IS NOT NULL AS has_job,
       input_tokens, output_tokens
FROM usage_ledger
ORDER BY created_at DESC
LIMIT 5"
