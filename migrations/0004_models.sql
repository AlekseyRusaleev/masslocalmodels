-- 0004_models.sql — каталог моделей, ревизии и артефакты.
--
-- Соответствует плану §5.1 и §19.2: логическая модель → ревизия с закреплённым
-- источником и лицензией → артефакты с URL, размером и SHA-256.
-- Узел скачивает артефакты сам по манифесту, координатор байты не передаёт.

CREATE TABLE IF NOT EXISTS model_catalog (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    display_name text        NOT NULL DEFAULT '',
    task_type    text        NOT NULL DEFAULT 'llm',
    status       text        NOT NULL DEFAULT 'active',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_catalog_status_check CHECK (status IN ('active', 'archived'))
);

CREATE UNIQUE INDEX IF NOT EXISTS model_catalog_name_uniq ON model_catalog (lower(name));

CREATE TABLE IF NOT EXISTS model_revisions (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id        uuid        NOT NULL REFERENCES model_catalog (id) ON DELETE CASCADE,
    source          text        NOT NULL DEFAULT '',
    revision        text        NOT NULL DEFAULT '',
    license         text        NOT NULL DEFAULT '',
    license_checked boolean     NOT NULL DEFAULT false,
    runtime         text        NOT NULL DEFAULT 'llama.cpp',
    quantization    text        NOT NULL DEFAULT '',
    size_bytes      bigint      NOT NULL DEFAULT 0,
    status          text        NOT NULL DEFAULT 'draft',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_revisions_status_check CHECK (status IN
        ('draft', 'ready', 'active', 'retired'))
);

CREATE INDEX IF NOT EXISTS model_revisions_model_idx ON model_revisions (model_id, created_at DESC);

CREATE TABLE IF NOT EXISTS model_artifacts (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id uuid        NOT NULL REFERENCES model_revisions (id) ON DELETE CASCADE,
    name        text        NOT NULL,
    url         text        NOT NULL,
    size_bytes  bigint      NOT NULL DEFAULT 0,
    sha256      text        NOT NULL DEFAULT '',
    ordinal     integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT model_artifacts_sha_check CHECK (sha256 = '' OR length(sha256) = 64)
);

CREATE UNIQUE INDEX IF NOT EXISTS model_artifacts_revision_name_uniq
    ON model_artifacts (revision_id, name);

COMMENT ON TABLE model_artifacts IS
    'Файлы ревизии. Узел скачивает их напрямую по url и сверяет sha256; координатор байты не передаёт.';
COMMENT ON COLUMN model_revisions.revision IS
    'Закреплённая ревизия источника (commit hash), а не плавающая ветка.';
