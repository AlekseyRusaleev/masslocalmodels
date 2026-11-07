#!/usr/bin/env bash
# ct-stage1.sh — этап 1 Control Plane внутри CT 108 (compute-control).
#
# Ставит PostgreSQL 17, NATS Server и Go API Gateway как системные службы.
# Идемпотентен: повторный запуск не ломает установленное и не меняет пароли.
#
# Ожидает исходники в $SRC (по умолчанию /root/stage1-src):
#   cmd/api-gateway/main.go, internal/httpapi/server.go, go.mod,
#   migrations/*.sql, scripts/migrate.sh, deploy/control/api-gateway.service
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

SRC="${SRC:-/root/stage1-src}"
PREFIX=/opt/compute-control

echo "== packages =="
apt-get update
apt-get install -y --no-install-recommends postgresql nats-server golang-go

echo "== service account and directories =="
id -u control >/dev/null 2>&1 || useradd --system --home "$PREFIX" --shell /usr/sbin/nologin control
install -d -m 0755 "$PREFIX" "$PREFIX/bin" "$PREFIX/migrations"
install -d -o control -g control -m 0750 /var/log/compute-control
install -d -m 0755 /etc/compute-control

echo "== database role and schema =="
DB_PASS_FILE=/etc/compute-control/.pg_control_secret
if [ ! -s "$DB_PASS_FILE" ]; then
    head -c 32 /dev/urandom | base64 | tr -d '/+=\n' | cut -c1-28 > "$DB_PASS_FILE"
    chmod 600 "$DB_PASS_FILE"
fi
DB_PASS="$(cat "$DB_PASS_FILE")"

if [ "$(runuser -u postgres -- psql -tAc "SELECT count(*) FROM pg_roles WHERE rolname = 'control'")" = "0" ]; then
    runuser -u postgres -- psql -q -c "CREATE ROLE control LOGIN PASSWORD '$DB_PASS'"
else
    runuser -u postgres -- psql -q -c "ALTER ROLE control PASSWORD '$DB_PASS'"
fi

if [ "$(runuser -u postgres -- psql -tAc "SELECT count(*) FROM pg_database WHERE datname = 'compute_control'")" = "0" ]; then
    runuser -u postgres -- createdb -O control compute_control
fi

echo "== environment file =="
cat > /etc/compute-control/api-gateway.env <<ENVFILE
LISTEN_ADDR=127.0.0.1:8080
DATABASE_URL=postgres://control:${DB_PASS}@127.0.0.1:5432/compute_control?sslmode=disable
NATS_URL=nats://127.0.0.1:4222
ENVFILE
chmod 600 /etc/compute-control/api-gateway.env
chown root:root /etc/compute-control/api-gateway.env

echo "== build api-gateway =="
install -m 0644 "$SRC"/migrations/*.sql "$PREFIX/migrations/"
install -m 0755 "$SRC/scripts/migrate.sh" "$PREFIX/migrate.sh"
install -m 0644 "$SRC/deploy/control/api-gateway.service" /etc/systemd/system/api-gateway.service
cd "$SRC"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$PREFIX/bin/api-gateway" ./cmd/api-gateway
chmod 0755 "$PREFIX/bin/api-gateway"

echo "== services =="
systemctl daemon-reload
systemctl enable --now postgresql
systemctl enable --now nats-server

DATABASE_URL="$(grep '^DATABASE_URL=' /etc/compute-control/api-gateway.env | cut -d= -f2-)" \
    "$PREFIX/migrate.sh"

systemctl enable --now api-gateway
sleep 2

echo "== verification =="
systemctl is-active postgresql nats-server api-gateway
runuser -u postgres -- psql -d compute_control -tAc "SELECT version FROM schema_migrations ORDER BY version"
curl -fsS http://127.0.0.1:8080/health; echo
curl -fsS http://127.0.0.1:8080/v1/models; echo
