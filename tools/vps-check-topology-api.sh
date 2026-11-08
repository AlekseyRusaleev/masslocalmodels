#!/bin/sh
set -a
. /etc/compute-control/api-gateway.env
set +a
TOKEN=$(runuser -u control -- /opt/compute-control/bin/api-gateway bootstrap-admin "$1" 2>&1 | awk '/token:/ {print $2}')
curl -s -m 10 -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8090/admin/v1/topologies \
  | python3 -c 'import json,sys
d=json.load(sys.stdin)["data"]
print("топологий:", len(d))
for item in d[:5]:
    t=item["topology"]
    print(" ", t["status"], "|", t.get("model_name"), "|", (t.get("error") or "—")[:100])'