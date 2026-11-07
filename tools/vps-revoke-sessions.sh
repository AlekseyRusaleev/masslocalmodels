#!/usr/bin/env bash
# Завершает все активные сессии панели. Нужно после проверок: тестовые входы
# не должны висеть в разделе «Активные входы».
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE auth_sessions SET revoked_at = now() WHERE revoked_at IS NULL"
psql "$DATABASE_URL" -tAc "SELECT 'активных сессий: ' || count(*) FROM auth_sessions WHERE revoked_at IS NULL"
