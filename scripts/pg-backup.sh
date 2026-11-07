#!/usr/bin/env bash
# pg-backup.sh — резервная копия базы Control Plane.
#
# Кладёт gzip-дамп в /var/backups/compute-control и хранит 7 дней.
# Не трогает базы других проектов на этой машине.
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/var/backups/compute-control}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
DB="${PGDATABASE:-compute_control}"
TS="$(date +%Y%m%d_%H%M%S)"
OUT="${BACKUP_DIR}/${DB}_${TS}.sql.gz"

mkdir -p "$BACKUP_DIR"
chmod 0750 "$BACKUP_DIR"

# Дамп снимается от суперпользователя postgres: роль control читает только свою БД,
# но pg_dump требует доступа к служебным каталогам.
runuser -u postgres -- pg_dump "$DB" | gzip -1 > "$OUT.part"
mv "$OUT.part" "$OUT"
chmod 0640 "$OUT"

echo "backup: ${OUT} ($(du -h "$OUT" | cut -f1))"
find "$BACKUP_DIR" -name "${DB}_*.sql.gz" -mtime "+${RETENTION_DAYS}" -delete
echo "retention: ${RETENTION_DAYS} days"

# Проверка целостности: архив должен читаться и содержать начало дампа.
if gzip -t "$OUT" && zcat "$OUT" | head -3 | grep -q 'PostgreSQL database dump'; then
    echo "verify: ok"
else
    echo "verify: FAILED" >&2
    exit 1
fi
