#!/usr/bin/env bash
# migrate.sh — применение SQL-миграций Control Plane.
#
# Версии фиксируются в таблице schema_migrations, повторный запуск безопасен.
# Каждая миграция применяется в одной транзакции: при ошибке ничего не остаётся.
set -euo pipefail

MIGRATIONS_DIR="${MIGRATIONS_DIR:-/opt/compute-control/migrations}"
: "${DATABASE_URL:?DATABASE_URL must be set}"

PSQL=(psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q)

"${PSQL[@]}" -c "CREATE TABLE IF NOT EXISTS schema_migrations (
    version    text        PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);"

shopt -s nullglob
applied_count=0
for file in "$MIGRATIONS_DIR"/*.sql; do
    version="$(basename "$file" .sql)"
    already="$("${PSQL[@]}" -tAc "SELECT count(*) FROM schema_migrations WHERE version = '$version'")"
    if [ "$already" != "0" ]; then
        echo "skip  $version"
        continue
    fi

    echo "apply $version"
    staged="$(mktemp)"
    {
        echo "BEGIN;"
        cat "$file"
        printf "\nINSERT INTO schema_migrations (version) VALUES ('%s');\n" "$version"
        echo "COMMIT;"
    } > "$staged"
    "${PSQL[@]}" -f "$staged"
    rm -f "$staged"
    applied_count=$((applied_count + 1))
done

echo "migrations complete (applied: $applied_count)"
