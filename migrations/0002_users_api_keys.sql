-- 0002_users_api_keys.sql — пользователи и API-ключи.
--
-- Соответствует плану §10.5 и §19.1:
--   - ключ показывается один раз, в БД хранится только keyed hash (HMAC-SHA256);
--   - по открытому значению ключа индексируется только key_id (префикс), не секрет;
--   - срок жизни, отзыв и дата последнего использования обязательны.

CREATE TABLE IF NOT EXISTS users (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email       text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'suspended', 'deleted'))
);

CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_uniq ON users (lower(email));

CREATE TABLE IF NOT EXISTS api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key_id       text        NOT NULL,
    key_hash     bytea       NOT NULL,
    name         text        NOT NULL DEFAULT '',
    scopes       text[]      NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    CONSTRAINT api_keys_key_id_check CHECK (length(key_id) BETWEEN 8 AND 32)
);

CREATE UNIQUE INDEX IF NOT EXISTS api_keys_key_id_uniq ON api_keys (key_id);
CREATE INDEX IF NOT EXISTS api_keys_user_id_idx ON api_keys (user_id);

COMMENT ON TABLE users IS 'Аккаунты платформы. Пароли не хранятся: вход по API-ключу.';
COMMENT ON COLUMN api_keys.key_hash IS 'HMAC-SHA256(pepper, secret). Исходный секрет не сохраняется.';
COMMENT ON COLUMN api_keys.scopes IS 'Например: models:read, chat:write, usage:read, admin.';
