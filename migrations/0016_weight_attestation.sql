-- 0016_weight_attestation.sql — сверка весов с манифестом каталога.
--
-- Зачем: узел уже сообщает имя модели и digest загруженных весов, но сервер
-- только записывал их рядом с заданием. Проверка появляется теперь: если в
-- каталоге есть ревизия этой модели с закреплённым sha256, digest узла
-- сверяется с ним, и задание помечается проверенным — или отклоняется.

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS digest_verified boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN jobs.digest_verified IS
    'true — digest весов узла совпал с манифестом каталога. false — манифеста нет или сверка не выполнялась.';

-- Что узел держит локально: модель → digest весов. Нужно панели (видно, что
-- именно загружено) и для раннего предупреждения: узел заявил модель, но
-- digest не тот, что в каталоге — задание не должно до него доехать.
CREATE TABLE IF NOT EXISTS worker_model_digests (
    worker_id   uuid        NOT NULL REFERENCES worker_nodes (id) ON DELETE CASCADE,
    model       text        NOT NULL,
    digest      text        NOT NULL DEFAULT '',
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (worker_id, model)
);

COMMENT ON TABLE worker_model_digests IS
    'Digest весов моделей, о которых узел сообщил в последнем heartbeat (без префикса sha256:).';
