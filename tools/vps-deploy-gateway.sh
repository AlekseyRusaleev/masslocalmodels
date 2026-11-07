#!/bin/sh
# Выкладка api-gateway: миграции, замена бинарника, перезапуск, проверка.
# Запуск: python tools/ssh_put.py tools/vps-deploy-gateway.sh /tmp/deploy.sh \
#         && python tools/ssh_run.py "sh /tmp/deploy.sh"
set -e

echo "== миграции =="
set -a
. /etc/compute-control/api-gateway.env
set +a
/opt/compute-control/migrate.sh

echo "== колонка mesh_rpc_addr =="
runuser -u postgres -- psql -d compute_control -tAc \
  "select column_name from information_schema.columns
   where table_name = 'worker_nodes' and column_name = 'mesh_rpc_addr'"

echo "== замена бинарника =="
if [ -f /opt/compute-control/bin/api-gateway.new ]; then
  mv /opt/compute-control/bin/api-gateway.new /opt/compute-control/bin/api-gateway
  chmod 0755 /opt/compute-control/bin/api-gateway
fi
systemctl restart api-gateway
sleep 2
systemctl is-active api-gateway

echo "== соседи живы =="
systemctl is-active nasushno.service nginx.service postgresql@16-main.service

echo "== health =="
curl -s -m 5 http://127.0.0.1:8090/health
echo
