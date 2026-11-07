-- 0010_job_usage.sql — реальные счётчики токенов и причина остановки.
--
-- Зачем: раньше ответ /v1/chat/completions возвращал usage с нулями, потому что
-- Control Plane не знал, сколько токенов посчитал рантайм. Узел теперь присылает
-- эти цифры вместе с завершением задания, так что их можно показывать честно.

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS prompt_tokens     integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS completion_tokens integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS eval_ms           bigint  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS finish_reason     text    NOT NULL DEFAULT '';

COMMENT ON COLUMN jobs.prompt_tokens IS
    'Токенов во входе: как их посчитал рантайм узла (Ollama prompt_eval_count).';
COMMENT ON COLUMN jobs.completion_tokens IS
    'Токенов в ответе: как их посчитал рантайм узла (Ollama eval_count).';
COMMENT ON COLUMN jobs.eval_ms IS
    'Сколько миллисекунд узел генерировал ответ (Ollama eval_duration), без загрузки модели.';
COMMENT ON COLUMN jobs.finish_reason IS
    'Почему генерация остановилась: stop, length (кончился num_predict), unload и т.п.';

CREATE INDEX IF NOT EXISTS jobs_usage_idx ON jobs (created_at DESC)
    WHERE completion_tokens > 0;
