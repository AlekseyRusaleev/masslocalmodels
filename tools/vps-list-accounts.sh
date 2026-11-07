#!/usr/bin/env bash
# Печатает аккаунты панели: кто есть и задан ли пароль.
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -tAc "SELECT id || ' | ' || email || ' | ' || role || ' | пароль: ' ||
  CASE WHEN password_hash = '' THEN 'нет' ELSE 'задан' END
  FROM users ORDER BY created_at"
echo "--- сессии ---"
psql "$DATABASE_URL" -tAc "SELECT count(*) FROM auth_sessions"
