#!/usr/bin/env bash
# Создаёт временный административный аккаунт для автопроверок панели.
#
#   bash /tmp/probe-create.sh <email> <password>
#
# Нужен, чтобы проверки не зависели от пароля владельца: он его меняет, и тест
# перестаёт входить. Аккаунт удаляется скриптом probe-delete.sh.
set -euo pipefail

email="${1:?нужна почта}"
password="${2:?нужен пароль}"

case "$email" in
  *\'*|*\\*) echo "в почте недопустимый символ" >&2; exit 2 ;;
esac

set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c \
  "INSERT INTO users (email, name, role) VALUES ('$email', 'Проверка панели', 'admin')
   ON CONFLICT (lower(email)) DO UPDATE SET role = 'admin', name = 'Проверка панели'"

/opt/compute-control/bin/api-gateway set-password "$email" "$password"

psql "$DATABASE_URL" -tAc "SELECT email || ' | ' || role || ' | пароль: ' ||
  CASE WHEN password_hash = '' THEN 'нет' ELSE 'задан' END FROM users ORDER BY created_at"
