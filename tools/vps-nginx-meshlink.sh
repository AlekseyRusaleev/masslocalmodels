#!/bin/sh
# Добавляет в vhost api.nshno.tech отдельный location для туннеля шард-группы.
#
# Зачем: тело запроса туннеля — поток тензоров и активаций, его размер заранее
# неизвестен. Общий client_max_body_size 16m обрезал бы поток на середине, а
# llama.cpp ждал бы ответа, которого не будет. Здесь размер не ограничивается.
set -e

CONF=/etc/nginx/sites-enabled/ai-platform
MARK=/worker/v1/mesh/link

if grep -q "$MARK" "$CONF"; then
  echo "already present"
  exit 0
fi

cp "$CONF" "$CONF.bak-mesh"
python3 - "$CONF" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")

block = """    # Туннель шард-группы: тело запроса — поток тензоров и активаций, размер
    # заранее неизвестен, поэтому лимит снят. Таймауты как у чата: соединение
    # живёт, пока идёт генерация.
    location /worker/v1/mesh/link {
        client_max_body_size 0;
        proxy_pass http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_request_buffering off;
        proxy_read_timeout 1800s;
        proxy_send_timeout 1800s;
    }

"""

anchor = "    location / {"
index = text.find(anchor)
if index < 0:
    raise SystemExit("не найден location / в конфиге")
path.write_text(text[:index] + block + text[index:], encoding="utf-8")
print("patched")
PY

nginx -t
systemctl reload nginx
echo "reloaded"
