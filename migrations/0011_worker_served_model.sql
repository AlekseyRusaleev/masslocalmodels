-- 0011_worker_served_model.sql — какая модель закреплена за узлом.
--
-- Зачем: до сих пор задание мог забрать любой свободный узел, даже если модель
-- у него не загружена. На одном узле это не проявлялось, с двумя — сразу:
-- запрос к qwen3:8b уезжал бы на машину, которая его не обслуживает.
-- Агент и раньше присылал имя модели в метаданных heartbeat; теперь оно лежит
-- в отдельном столбце, по нему и идёт выдача задания.

ALTER TABLE worker_nodes ADD COLUMN IF NOT EXISTS served_model text NOT NULL DEFAULT '';

COMMENT ON COLUMN worker_nodes.served_model IS
    'Модель, которую узел заявил в последнем heartbeat. Пустая строка — узел не обслуживает чат-задания.';

CREATE INDEX IF NOT EXISTS worker_nodes_served_model_idx
    ON worker_nodes (served_model)
    WHERE state = 'available' AND served_model <> '';
