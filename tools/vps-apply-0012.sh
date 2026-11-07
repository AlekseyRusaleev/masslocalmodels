#!/usr/bin/env bash
# Применяет миграцию 0012 и проверяет схему. Идемпотентно.
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

MIGRATION=/opt/compute-control/migrations/0012_account_sessions.sql

if [ -x /opt/compute-control/bin/migrate.sh ]; then
  /opt/compute-control/bin/migrate.sh
else
  # migrate.sh не установлен: применяем точечно, запись о версии — вручную.
  already=$(psql "$DATABASE_URL" -tAc "SELECT count(*) FROM schema_migrations WHERE version = '0012_account_sessions'" 2>/dev/null || echo 0)
  if [ "$already" = "1" ]; then
    echo "skip  0012_account_sessions"
  else
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -f "$MIGRATION"
    psql "$DATABASE_URL" -q -c "INSERT INTO schema_migrations (version) VALUES ('0012_account_sessions') ON CONFLICT DO NOTHING"
    echo "apply 0012_account_sessions"
  fi
fi

echo "--- колонки auth_sessions ---"
psql "$DATABASE_URL" -tAc "SELECT string_agg(column_name, ', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'auth_sessions'"
echo "--- users: новые колонки ---"
psql "$DATABASE_URL" -tAc "SELECT string_agg(column_name, ', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'users'"
echo "--- worker_nodes.num_ctx ---"
psql "$DATABASE_URL" -tAc "SELECT count(*) FROM worker_nodes"
echo "--- схема migrations ---"
psql "$DATABASE_URL" -tAc "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 3"
