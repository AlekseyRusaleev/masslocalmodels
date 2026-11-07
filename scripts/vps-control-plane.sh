#!/usr/bin/env bash
# vps-control-plane.sh — этап 1 Control Plane на арендованном VPS (144.31.61.208).
#
# Ставит NATS + Go API рядом с работающим проектом владельца (`nasushno`):
#   - PostgreSQL не устанавливается: используется существующий кластер 16,
#     отдельная роль `control` и база `compute_control`;
#   - API слушает 127.0.0.1:8090 (8080 занят `nasushno-api`);
#   - nginx, ufw, systemd-юниты и база владельца не изменяются.
#
# Идемпотентен: повторный запуск не ломает установленное и не меняет пароль.
# Ожидает исходники в $SRC (по умолчанию /root/stage1-src).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

SRC="${SRC:-/root/stage1-src}"
PREFIX="${PREFIX:-/opt/compute-control}"
LISTEN_ADDR="${LISTEN_ADDR:-127.0.0.1:8090}"

echo "== packages (nats, go) =="
apt-get update
apt-get install -y --no-install-recommends nats-server golang-go rsync

echo "== service account and directories =="
id -u control >/dev/null 2>&1 || useradd --system --home "$PREFIX" --shell /usr/sbin/nologin control
install -d -m 0755 "$PREFIX" "$PREFIX/bin" "$PREFIX/migrations"
install -d -o control -g control -m 0750 /var/log/compute-control
install -d -m 0755 /etc/compute-control
install -d -m 0750 /var/backups/compute-control

echo "== database role and database (existing PostgreSQL cluster) =="
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
PEPPER_FILE=/etc/compute-control/.api_pepper
if [ ! -s "$PEPPER_FILE" ]; then
    head -c 48 /dev/urandom | base64 | tr -d '/+=\n' > "$PEPPER_FILE"
    chmod 600 "$PEPPER_FILE"
fi
API_PEPPER="$(cat "$PEPPER_FILE")"

cat > /etc/compute-control/api-gateway.env <<ENVFILE
LISTEN_ADDR=${LISTEN_ADDR}
DATABASE_URL=postgres://control:${DB_PASS}@127.0.0.1:5432/compute_control?sslmode=disable
NATS_URL=nats://127.0.0.1:4222
API_PEPPER=${API_PEPPER}
ENVFILE
chmod 600 /etc/compute-control/api-gateway.env
chown root:root /etc/compute-control/api-gateway.env

echo "== nats binds to loopback only =="
if [ -f /etc/nats-server.conf ]; then
    if ! grep -q '^host:' /etc/nats-server.conf; then
        printf 'host: 127.0.0.1\nport: 4222\n' >> /etc/nats-server.conf
        echo "appended host binding to /etc/nats-server.conf"
    fi
else
    printf 'host: 127.0.0.1\nport: 4222\n' > /etc/nats-server.conf
fi

echo "== build api-gateway =="
install -m 0644 "$SRC"/migrations/*.sql "$PREFIX/migrations/"
install -m 0755 "$SRC/scripts/migrate.sh" "$PREFIX/migrate.sh"
install -m 0644 "$SRC/deploy/control/api-gateway.service" /etc/systemd/system/api-gateway.service
cd "$SRC"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$PREFIX/bin/api-gateway" ./cmd/api-gateway
chmod 0755 "$PREFIX/bin/api-gateway"

echo "== services =="
systemctl daemon-reload
systemctl enable --now nats-server

DATABASE_URL="$(grep '^DATABASE_URL=' /etc/compute-control/api-gateway.env | cut -d= -f2-)" \
    "$PREFIX/migrate.sh"

systemctl enable --now api-gateway
sleep 2

echo "== verification =="
systemctl is-active nats-server api-gateway
curl -fsS "http://${LISTEN_ADDR}/health"; echo
curl -fsS "http://${LISTEN_ADDR}/v1/models"; echo
echo "== neighbours untouched =="
systemctl is-active nasushno.service nginx.service postgresql@16-main.service
