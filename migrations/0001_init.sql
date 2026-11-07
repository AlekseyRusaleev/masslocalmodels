-- 0001_init.sql — служебный каркас схемы.
--
-- На этом шаге намеренно не описываются пользовательские таблицы: модель данных
-- (users, projects, conversations, tasks, model_registry...) проектируется
-- отдельными миграциями после этапа 1, как требует план §19.
--
-- Версии миграций записывает scripts/migrate.sh в schema_migrations.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    text        PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- Реестр запущенных экземпляров Control Plane: нужен для мониторинга и
-- проверки, что после рестарта поднялась та же версия.
CREATE TABLE IF NOT EXISTS service_instances (
    id          bigserial   PRIMARY KEY,
    name        text        NOT NULL,
    version     text        NOT NULL,
    started_at  timestamptz NOT NULL DEFAULT now(),
    metadata    jsonb       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS service_instances_name_started_idx
    ON service_instances (name, started_at DESC);

COMMENT ON TABLE service_instances IS
    'Запуски сервисов Control Plane. Содержит только технические метаданные.';
