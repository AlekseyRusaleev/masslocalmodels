#!/bin/sh
# Применяет миграции и проверяет, что колонка topologies.error появилась.
set -a
. /etc/compute-control/api-gateway.env
set +a

/opt/compute-control/migrate.sh 2>&1 | tail -3

echo "=== колонка topologies.error ==="
runuser -u postgres -- psql -d compute_control -tAc \
  "SELECT column_name FROM information_schema.columns WHERE table_name = 'topologies' AND column_name = 'error'"

echo "=== служба и здоровье ==="
systemctl is-active api-gateway
curl -s -m 5 http://127.0.0.1:8090/health
echo
echo "=== топологии через API (нужен админ-ключ, проверяем только код ответа) ==="
curl -s -o /dev/null -w "topologies: %{http_code}\n" -m 5 http://127.0.0.1:8090/admin/v1/topologies
echo "=== соседи ==="
systemctl is-active nginx nasushno.service postgresql@16-main.service
