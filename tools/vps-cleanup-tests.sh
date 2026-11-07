#!/usr/bin/env bash
# Убирает следы автопроверок: служебные аккаунты и синтетические задания.
#
#   bash /tmp/cleanup-tests.sh
set -euo pipefail

set -a
. /etc/compute-control/api-gateway.env
set +a

echo "--- до уборки ---"
psql "$DATABASE_URL" -tAc "SELECT string_agg(email || ' (' || role || ', ' || status || ')', ', ') FROM users"

# Служебные аккаунты автотестов: создаются проверками и удаляются ими же, но
# прерванный прогон оставляет их. Настоящие аккаунты здесь не попадают.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "DELETE FROM users WHERE email LIKE 'check-%@nshno.tech' OR email LIKE 'fair-%@nshno.tech'"

# Синтетические ревизии каталога из тестов аттестации и заглушек.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "DELETE FROM model_catalog WHERE name LIKE 'attest-%' OR name LIKE 'stub-%' OR name LIKE 'fair-%'"

# Синтетические задания: модели-заглушки, которых в сети нет.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "DELETE FROM jobs WHERE model LIKE 'stub-%' OR model LIKE 'fair-%' OR model LIKE 'attest-%'"

echo "--- после уборки ---"
psql "$DATABASE_URL" -tAc "SELECT 'аккаунты: ' || string_agg(email || ' (' || role || ')', ', ' ORDER BY email) FROM users"
psql "$DATABASE_URL" -tAc "SELECT 'узлы: ' || string_agg(name || ' ' || state, ', ' ORDER BY name) FROM worker_nodes"
psql "$DATABASE_URL" -tAc "SELECT 'учёт: заданий ' || count(*) || ', токенов ' || COALESCE(sum(completion_tokens), 0) FROM usage_ledger"
psql "$DATABASE_URL" -tAc "SELECT 'заданий в журнале: ' || count(*) FROM jobs"
psql "$DATABASE_URL" -tAc "SELECT 'диалогов: ' || count(*) FROM conversations"
psql "$DATABASE_URL" -tAc "SELECT 'ревизий каталога: ' || count(*) FROM model_revisions"
psql "$DATABASE_URL" -tAc "SELECT 'активных сессий: ' || count(*) FROM auth_sessions WHERE revoked_at IS NULL"
