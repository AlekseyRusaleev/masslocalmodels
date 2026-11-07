-- 0007_worker_links.sql — измеренное качество связи узла и размер модели.
--
-- Зачем: Scheduler обязан отличать быстрый локальный кластер от распределённого
-- через WAN (ADR 0010). Агент измеряет RTT своего heartbeat и сообщает его,
-- Control Plane хранит последнее значение. Размер модели в миллиардах параметров
-- нужен, чтобы показать ожидаемую скорость до запуска, а не после.

ALTER TABLE worker_nodes ADD COLUMN IF NOT EXISTS rtt_ms integer;
ALTER TABLE worker_nodes ADD COLUMN IF NOT EXISTS link_measured_at timestamptz;

COMMENT ON COLUMN worker_nodes.rtt_ms IS
    'Последний измеренный агентом RTT до Control Plane, миллисекунды.';

ALTER TABLE model_revisions ADD COLUMN IF NOT EXISTS params_billions real;

COMMENT ON COLUMN model_revisions.params_billions IS
    'Число параметров в миллиардах; используется для оценки скорости одиночного запроса.';
