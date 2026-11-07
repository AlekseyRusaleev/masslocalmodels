-- 0003_workers.sql — регистрация Worker-узлов, возможности, heartbeat.
--
-- Модель соответствует плану §6.1–6.2 и §19.2:
--   - enrollment одноразовый, короткоживущий, привязан к аккаунту владельца;
--   - после регистрации у узла свой секрет устройства (в БД только HMAC-хеш);
--   - возможности (CPU/RAM/GPU/диски/сеть) хранятся отдельно от heartbeat;
--   - heartbeat — история телеметрии без содержимого пользовательских задач.

CREATE TABLE IF NOT EXISTS worker_nodes (
    id                       uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id            uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name                     text        NOT NULL,
    state                    text        NOT NULL DEFAULT 'available',
    trust_class              text        NOT NULL DEFAULT 'private',
    agent_version            text        NOT NULL DEFAULT '',
    os                       text        NOT NULL DEFAULT '',
    hostname                 text        NOT NULL DEFAULT '',
    device_token_id          text        NOT NULL,
    device_token_hash        bytea       NOT NULL,
    certificate_fingerprint  text,
    enrolled_at              timestamptz NOT NULL DEFAULT now(),
    last_seen_at             timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT worker_nodes_state_check CHECK (state IN
        ('available', 'paused', 'drain', 'maintenance', 'quarantined', 'offline')),
    CONSTRAINT worker_nodes_trust_check CHECK (trust_class IN
        ('private', 'trusted', 'public'))
);

CREATE UNIQUE INDEX IF NOT EXISTS worker_nodes_device_token_id_uniq ON worker_nodes (device_token_id);
CREATE UNIQUE INDEX IF NOT EXISTS worker_nodes_owner_name_uniq ON worker_nodes (owner_user_id, lower(name));
CREATE INDEX IF NOT EXISTS worker_nodes_owner_idx ON worker_nodes (owner_user_id);

CREATE TABLE IF NOT EXISTS worker_enrollments (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_id      text        NOT NULL,
    token_hash    bytea       NOT NULL,
    label         text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    worker_id     uuid        REFERENCES worker_nodes (id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS worker_enrollments_token_id_uniq ON worker_enrollments (token_id);
CREATE INDEX IF NOT EXISTS worker_enrollments_owner_idx ON worker_enrollments (owner_user_id);

CREATE TABLE IF NOT EXISTS worker_capabilities (
    worker_id      uuid        PRIMARY KEY REFERENCES worker_nodes (id) ON DELETE CASCADE,
    cpu_model      text        NOT NULL DEFAULT '',
    cpu_cores      integer,
    ram_bytes      bigint,
    gpu_json       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    disk_json      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    cuda_version   text        NOT NULL DEFAULT '',
    driver_version text        NOT NULL DEFAULT '',
    network_json   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS worker_heartbeats (
    id               bigserial   PRIMARY KEY,
    worker_id        uuid        NOT NULL REFERENCES worker_nodes (id) ON DELETE CASCADE,
    observed_at      timestamptz NOT NULL DEFAULT now(),
    state            text        NOT NULL DEFAULT '',
    cpu_percent      real,
    ram_free_bytes   bigint,
    vram_free_bytes  bigint,
    disk_free_bytes  bigint,
    temperature_c    real,
    power_watts      real,
    active_jobs      integer,
    meta_json        jsonb       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS worker_heartbeats_worker_time_idx
    ON worker_heartbeats (worker_id, observed_at DESC);

COMMENT ON TABLE worker_enrollments IS
    'Одноразовые токены подключения устройства. Хранится только HMAC-хеш токена.';
COMMENT ON COLUMN worker_nodes.device_token_hash IS
    'HMAC-SHA256(pepper, device_secret). Исходный секрет устройства не сохраняется.';
COMMENT ON TABLE worker_heartbeats IS
    'Телеметрия узла. Не содержит промптов, путей к пользовательским файлам и содержимого задач.';
