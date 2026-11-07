-- 0006_artifact_layers.sql — артефакт знает свой диапазон слоёв.
--
-- Зачем: узел должен качать только те файлы, которые соответствуют его этапу
-- в топологии (ADR 0009: каждый держит свой диапазон блоков). Артефакты без
-- диапазона (config.json, tokenizer.json) считаются общими и нужны всем узлам.

ALTER TABLE model_artifacts ADD COLUMN IF NOT EXISTS layer_start integer;
ALTER TABLE model_artifacts ADD COLUMN IF NOT EXISTS layer_end integer;

COMMENT ON COLUMN model_artifacts.layer_start IS
    'Первый слой, содержащийся в файле. NULL — файл общий для всех узлов.';
COMMENT ON COLUMN model_artifacts.layer_end IS
    'Последний слой включительно. NULL — файл общий для всех узлов.';

CREATE INDEX IF NOT EXISTS model_artifacts_layers_idx
    ON model_artifacts (revision_id, layer_start, layer_end);
