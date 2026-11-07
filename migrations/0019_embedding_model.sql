-- 0019_embedding_model.sql — узел может обслуживать модель эмбеддингов отдельно.
--
-- Зачем: чат и эмбеддинги — разные модели. Держать их в одном поле
-- served_model нельзя: тогда узел либо не отвечает на эмбеддинги, либо
-- перестаёт обслуживать чат. Второе поле решает это без смешивания.

ALTER TABLE worker_nodes ADD COLUMN IF NOT EXISTS served_embedding_model text NOT NULL DEFAULT '';

COMMENT ON COLUMN worker_nodes.served_embedding_model IS
    'Модель эмбеддингов, которую узел объявил в последнем heartbeat. Пустая строка — не обслуживает.';

CREATE INDEX IF NOT EXISTS worker_nodes_served_embedding_idx
    ON worker_nodes (served_embedding_model)
    WHERE state = 'available' AND served_embedding_model <> '';
