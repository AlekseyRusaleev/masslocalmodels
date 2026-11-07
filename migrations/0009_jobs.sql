-- 0009_jobs.sql — задания инференса между API и узлами.
--
-- Зачем: узлы за NAT, поэтому Control Plane не может обратиться к ним напрямую.
-- Задание создаётся на сервере, узел сам забирает его long-poll'ом, стримит
-- чанки обратно и закрывает задание. Состояние хранится в БД, чтобы задание
-- переживало рестарт API.

CREATE TABLE IF NOT EXISTS jobs (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    worker_id    uuid        REFERENCES worker_nodes (id) ON DELETE SET NULL,
    model        text        NOT NULL DEFAULT '',
    kind         text        NOT NULL DEFAULT 'chat',
    payload      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state        text        NOT NULL DEFAULT 'queued',
    created_at   timestamptz NOT NULL DEFAULT now(),
    claimed_at   timestamptz,
    finished_at  timestamptz,
    error        text        NOT NULL DEFAULT '',
    CONSTRAINT jobs_state_check CHECK (state IN
        ('queued', 'claimed', 'streaming', 'done', 'failed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS jobs_queue_idx ON jobs (state, created_at) WHERE state = 'queued';
CREATE INDEX IF NOT EXISTS jobs_worker_idx ON jobs (worker_id, created_at DESC);

COMMENT ON TABLE jobs IS
    'Задания инференса. Узел забирает задание сам; чанки идут обратно отдельными вызовами.';
