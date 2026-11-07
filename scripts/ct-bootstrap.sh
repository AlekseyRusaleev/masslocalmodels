#!/usr/bin/env bash
# ct-bootstrap.sh — базовая настройка compute-control (CT 108).
# Идемпотентен: повторный запуск не ломает установленное.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "== base packages =="
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg git jq htop unzip dnsutils tcpdump make \
  tzdata rsync less nano

echo "== timezone Europe/Moscow =="
ln -sf /usr/share/zoneinfo/Europe/Moscow /etc/localtime
echo "Europe/Moscow" > /etc/timezone
dpkg-reconfigure -f noninteractive tzdata >/dev/null 2>&1 || true

echo "== docker repository =="
install -m 0755 -d /etc/apt/keyrings
if [ ! -f /etc/apt/keyrings/docker.asc ]; then
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
fi
chmod a+r /etc/apt/keyrings/docker.asc
ARCH="$(dpkg --print-architecture)"
CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian %s stable\n' \
  "$ARCH" "$CODENAME" > /etc/apt/sources.list.d/docker.list
apt-get update

echo "== docker engine =="
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

systemctl enable --now docker
sleep 2

echo "== versions =="
docker --version
docker compose version
systemctl is-active docker
