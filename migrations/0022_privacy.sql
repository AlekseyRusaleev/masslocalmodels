-- 0022_privacy.sql — чей запрос и закрытая история.
--
-- private: задание забирает только узел того же владельца.
-- shared: автор явно отдал текст сети, его может взять чужой узел.
-- История диалога хранит шифротекст. Ключ на сервер не приходит.

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS visibility text NOT NULL DEFAULT 'private';

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_visibility_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_visibility_check CHECK (visibility IN ('private', 'shared'));

ALTER TABLE conversations
    ADD COLUMN IF NOT EXISTS title_nonce text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS title_key_id text NOT NULL DEFAULT '';

COMMENT ON COLUMN conversations.title_key_id IS
    'Если непусто, title — шифротекст заголовка, а не открытый текст.';

COMMENT ON COLUMN jobs.visibility IS
    'private — только узлы владельца запроса; shared — автор согласился, что текст увидит чужой узел.';

ALTER TABLE conversation_messages
    ADD COLUMN IF NOT EXISTS key_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS nonce text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ciphertext text NOT NULL DEFAULT '';

COMMENT ON COLUMN conversation_messages.ciphertext IS
    'Шифротекст сообщения (AES-GCM). content и reasoning для новых записей пустые.';
COMMENT ON COLUMN conversation_messages.key_id IS
    'Идентификатор ключа на компьютере участника. Сам ключ здесь не хранится.';
