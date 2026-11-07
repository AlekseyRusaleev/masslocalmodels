#!/usr/bin/env bash
# Устанавливает новый api-gateway и перезапускает службу.
set -euo pipefail

NEW=/tmp/api-gateway-new
TARGET=/opt/compute-control/bin/api-gateway

before=$(systemctl show -p ActiveEnterTimestamp --value api-gateway)
echo "было запущено: $before"

install -m 0755 "$NEW" "$TARGET.new"
mv -f "$TARGET.new" "$TARGET"
echo "бинарник обновлён: $(sha256sum "$TARGET" | cut -c1-16)"

systemctl restart api-gateway
sleep 2
echo "состояние: $(systemctl is-active api-gateway)"
echo "версия: $(/opt/compute-control/bin/api-gateway version)"
echo "--- health ---"
curl -sS --max-time 5 http://127.0.0.1:8090/health
echo
echo "--- чужой проект не тронут ---"
systemctl show -p ActiveEnterTimestamp --value nasushno.service
