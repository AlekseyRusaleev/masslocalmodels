#!/usr/bin/env bash
# Удаляет временный аккаунт проверки вместе с его сессиями и диалогами.
#
#   bash /tmp/probe-delete.sh <email>
set -euo pipefail

email="${1:?нужна почта}"
case "$email" in
  *\'*|*\\*) echo "в почте недопустимый символ" >&2; exit 2 ;;
esac

set -a
. /etc/compute-control/api-gateway.env
set +a

# Связанные строки уходят каскадом: sessions и conversations ссылаются на users.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "DELETE FROM users WHERE lower(email) = lower('$email')"

echo "--- осталось аккаунтов ---"
psql "$DATABASE_URL" -tAc "SELECT email || ' | ' || role FROM users ORDER BY created_at"
echo "--- осталось диалогов ---"
psql "$DATABASE_URL" -tAc "SELECT 'диалогов: ' || count(*) FROM conversations"
