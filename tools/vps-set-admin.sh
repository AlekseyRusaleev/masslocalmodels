#!/usr/bin/env bash
# Даёт аккаунту роль администратора и задаёт пароль входа в панель.
#
#   bash /tmp/set-admin.sh <email> <password>
set -euo pipefail

email="${1:?нужна почта}"
password="${2:?нужен пароль}"

# Почта подставляется в текст SQL, поэтому кавычку и обратную косую черту в ней
# не пропускаем: иначе запрос можно сломать значением аргумента.
case "$email" in
  *\'*|*\\*) echo "в почте недопустимый символ" >&2; exit 2 ;;
esac

set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE users SET role = 'admin' WHERE lower(email) = lower('$email')"

/opt/compute-control/bin/api-gateway set-password "$email" "$password"

echo "--- итог ---"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -tAc "SELECT email || ' | ' || role || ' | пароль: ' ||
  CASE WHEN password_hash = '' THEN 'нет' ELSE 'задан' END FROM users ORDER BY created_at"
