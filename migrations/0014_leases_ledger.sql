-- 0014_leases_ledger.sql — этап 7: аренда задания, повторы, приоритеты, учёт.
--
-- Зачем по частям:
--   1. Аренда. Узел мог замолчать посреди ответа (закрыли ноутбук, пропала сеть),
--      и задание навсегда оставалось в состоянии «streaming»: клиент ждал, пока
--      его отпустит уже никем не обслуживаемый стрим. Теперь у захвата есть
--      срок; просроченное задание возвращается в очередь или закрывается.
--   2. Повторы. attempts считает выдачи: после N попыток задание закрывается
--      ошибкой, а не циклится вечно.
--   3. Приоритеты. Очередь была FIFO, поэтому один длинный пакетный запрос
--      задерживал интерактивные. Теперь порядок — приоритет, затем время.
--   4. Учёт. Начисления лежат отдельным журналом с уникальностью по заданию:
--      повторная доставка результата не удваивает вклад.
--   5. Подлинность. Узел сообщает, какую модель и с каким digest он реально
--      обслужил; сервер сверяет с запрошенной и хранит доказательство.

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS priority         integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attempts         integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS served_model     text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS served_digest    text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS requester_user_id uuid REFERENCES users (id) ON DELETE SET NULL;

COMMENT ON COLUMN jobs.priority IS
    'Больше — важнее. По умолчанию 0; очередь сортируется по приоритету, затем по времени создания.';
COMMENT ON COLUMN jobs.attempts IS
    'Сколько раз задание выдавалось узлам. Ограничивает бесконечные повторы.';
COMMENT ON COLUMN jobs.lease_expires_at IS
    'До какого момента задание закреплено за узлом. Просроченный захват возвращается в очередь.';
COMMENT ON COLUMN jobs.served_model IS
    'Имя модели, которое узел сообщил как фактически обслужившее задание.';
COMMENT ON COLUMN jobs.served_digest IS
    'Digest весов из рантайма узла (ollama: короткий sha256 загруженной модели).';

CREATE INDEX IF NOT EXISTS jobs_priority_queue_idx
    ON jobs (priority DESC, created_at)
    WHERE state = 'queued';

CREATE INDEX IF NOT EXISTS jobs_lease_idx
    ON jobs (lease_expires_at)
    WHERE state IN ('claimed', 'streaming');

-- Журнал учёта: одна запись на задание. Уникальность — гарантия, что повторная
-- доставка результата не начислит второй раз.
CREATE TABLE IF NOT EXISTS usage_ledger (
    id                bigserial   PRIMARY KEY,
    job_id            uuid        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    user_id           uuid        REFERENCES users (id) ON DELETE SET NULL,
    worker_id         uuid        REFERENCES worker_nodes (id) ON DELETE SET NULL,
    model             text        NOT NULL DEFAULT '',
    prompt_tokens     integer     NOT NULL DEFAULT 0,
    completion_tokens integer     NOT NULL DEFAULT 0,
    eval_ms           bigint      NOT NULL DEFAULT 0,
    recorded_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS usage_ledger_job_uniq ON usage_ledger (job_id);
CREATE INDEX IF NOT EXISTS usage_ledger_user_idx ON usage_ledger (user_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS usage_ledger_worker_idx ON usage_ledger (worker_id, recorded_at DESC);

COMMENT ON TABLE usage_ledger IS
    'Учёт вклада и потребления: токены по заданиям. Уникальность по job_id делает начисление идемпотентным.';
COMMENT ON COLUMN usage_ledger.user_id IS
    'Кто запросил задание (потребитель). NULL — задание без известного заказчика.';
COMMENT ON COLUMN usage_ledger.worker_id IS
    'Кто обслужил задание (вкладчик).';
