-- 0008_benchmarks.sql — калибровка узла: измеренная скорость декода.
--
-- Зачем: прогноз скорости топологии сейчас использует общую константу
-- «1.79 мс на 1 млрд параметров на RTX 5060». Для другой карты или CPU-узла
-- это неверно. Агент прогоняет короткий замер на своём железе и сообщает
-- результат; Scheduler нормирует его на размер эталонной модели.

ALTER TABLE worker_capabilities ADD COLUMN IF NOT EXISTS bench_model_billions real;
ALTER TABLE worker_capabilities ADD COLUMN IF NOT EXISTS bench_quantization text;
ALTER TABLE worker_capabilities ADD COLUMN IF NOT EXISTS bench_tokens_per_second real;
ALTER TABLE worker_capabilities ADD COLUMN IF NOT EXISTS bench_at timestamptz;

COMMENT ON COLUMN worker_capabilities.bench_tokens_per_second IS
    'Измеренная скорость декода на эталонной модели (bench_model_billions, bench_quantization).';
