#!/usr/bin/env bash
# Убирает пароль и все сессии аккаунта: возвращает его в состояние «вход только по ключу».
#
#   bash /tmp/clear-password.sh <email>
set -euo pipefail

email="${1:?нужна почта}"
case "$email" in
  *\'*|*\\*) echo "в почте недопустимый символ" >&2; exit 2 ;;
esac

set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE users SET password_hash = '' WHERE lower(email) = lower('$email')"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE auth_sessions SET revoked_at = now() WHERE user_id = (SELECT id FROM users WHERE lower(email) = lower('$email')) AND revoked_at IS NULL"

psql "$DATABASE_URL" -tAc "SELECT email || ' | пароль: ' ||
  CASE WHEN password_hash = '' THEN 'нет' ELSE 'задан' END FROM users ORDER BY created_at"
