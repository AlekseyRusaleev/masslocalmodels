#!/usr/bin/env bash
# Меняет роль аккаунта: admin или user.
#
#   bash /tmp/set-role.sh <email> <admin|user>
set -euo pipefail

email="${1:?нужна почта}"
role="${2:?нужна роль}"

case "$email" in
  *\'*|*\\*) echo "в почте недопустимый символ" >&2; exit 2 ;;
esac
case "$role" in
  admin|user) ;;
  *) echo "роль должна быть admin или user" >&2; exit 2 ;;
esac

set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE users SET role = '$role' WHERE lower(email) = lower('$email')"

psql "$DATABASE_URL" -tAc "SELECT email || ' | ' || role FROM users ORDER BY created_at"
