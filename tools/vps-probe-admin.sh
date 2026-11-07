#!/bin/sh
# Временный администратор для проверки туннеля: выпускает ключ и печатает его.
set -e
set -a
. /etc/compute-control/api-gateway.env
set +a
runuser -u control -- /opt/compute-control/bin/api-gateway bootstrap-admin "$1" 2>&1 | grep -e token -e key_id
