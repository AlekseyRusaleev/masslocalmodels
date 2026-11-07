#!/usr/bin/env bash
# Проверяет, какие расширения PostgreSQL доступны: от этого зависит,
# хранить эмбеддинги в pgvector или обычным массивом.
set -euo pipefail
set -a
. /etc/compute-control/api-gateway.env
set +a

psql "$DATABASE_URL" -tAc "SELECT 'available: ' || coalesce(string_agg(name, ', '), 'нет') FROM pg_available_extensions WHERE name IN ('vector', 'pg_trgm')"
psql "$DATABASE_URL" -tAc "SELECT 'installed: ' || coalesce(string_agg(extname, ', '), 'нет') FROM pg_extension WHERE extname IN ('vector', 'pg_trgm')"
