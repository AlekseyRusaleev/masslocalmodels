-- 0012_account_sessions.sql — вход в панель по паролю и окно контекста узла.
--
-- Зачем пароль: до сих пор панель открывалась только административным ключом,
-- то есть у каждого, кому дали доступ, был ключ со всеми правами. Теперь вход
-- идёт по email и паролю, а панель работает по сессии с ограниченным сроком.
--
-- Пароль хранится только как bcrypt-хеш (cost 12). Сессия — тоже пара
-- ключ+секрет, как API-ключ: в БД лежит HMAC-SHA256(pepper, secret), поэтому по
-- дампу БД войти нельзя.

ALTER TABLE users ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash text NOT NULL DEFAULT '';

-- Роль определяет набор scope для сессии панели.
ALTER TABLE users ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'user';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'admin'));

CREATE TABLE IF NOT EXISTS auth_sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key_id       text        NOT NULL,
    key_hash     bytea       NOT NULL,
    name         text        NOT NULL DEFAULT '',
    scopes       text[]      NOT NULL DEFAULT '{}',
    user_agent   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    CONSTRAINT auth_sessions_key_id_check CHECK (length(key_id) BETWEEN 8 AND 32)
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_sessions_key_id_uniq ON auth_sessions (key_id);
CREATE INDEX IF NOT EXISTS auth_sessions_user_id_idx ON auth_sessions (user_id);
CREATE INDEX IF NOT EXISTS auth_sessions_active_idx
    ON auth_sessions (expires_at)
    WHERE revoked_at IS NULL;

-- Окно контекста, которое узел держит под задания. Нужно панели и API, чтобы
-- клиент знал предел промпта до того, как узел откажет.
ALTER TABLE worker_nodes ADD COLUMN IF NOT EXISTS num_ctx integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.password_hash IS
    'bcrypt-хеш пароля панели. Пустая строка — вход по паролю не настроен.';
COMMENT ON TABLE auth_sessions IS
    'Сессии панели: срок жизни, отзыв, последнее использование. Токен показывается один раз.';
COMMENT ON COLUMN worker_nodes.num_ctx IS
    'Окно контекста узла в токенах из последнего heartbeat. 0 — узел не сообщил.';
