-- 0013_conversations.sql — история диалогов на Control Plane.
--
-- Зачем: панель хранила переписку только в браузере, поэтому диалог терялся
-- при смене устройства и не был виден с другого входа. Теперь разговор — это
-- запись платформы, привязанная к аккаунту.
--
-- Что НЕ хранится: ничего, кроме текста, счётчиков и модели. Файлы, изображения
-- и вложения появятся отдельной таблицей, когда появятся в API.

CREATE TABLE IF NOT EXISTS conversations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      text        NOT NULL DEFAULT '',
    model      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS conversations_user_idx ON conversations (user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS conversation_messages (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id   uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    ordinal           integer     NOT NULL,
    role              text        NOT NULL,
    content           text        NOT NULL DEFAULT '',
    reasoning         text        NOT NULL DEFAULT '',
    prompt_tokens     integer     NOT NULL DEFAULT 0,
    completion_tokens integer     NOT NULL DEFAULT 0,
    eval_ms           integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conversation_messages_role_check CHECK (role IN ('user', 'assistant', 'system'))
);

CREATE UNIQUE INDEX IF NOT EXISTS conversation_messages_order_uniq
    ON conversation_messages (conversation_id, ordinal);

COMMENT ON TABLE conversations IS
    'Диалог панели: принадлежит аккаунту, виден только ему.';
COMMENT ON COLUMN conversation_messages.reasoning IS
    'Размышления модели (reasoning_content). Отдельным полем: их видно в интерфейсе, но они не часть ответа.';
COMMENT ON COLUMN conversation_messages.ordinal IS
    'Порядковый номер внутри диалога: по нему сообщения восстанавливаются в исходном порядке.';
