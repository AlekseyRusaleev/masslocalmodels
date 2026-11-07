-- 0023_auto_topology.sql — топология, которую платформа собрала сама.
--
-- Ручная раскладка администратора не должна сниматься, когда в сети
-- временно меньше двух узлов с llama.cpp. Флаг отделяет автоматическую
-- группу слоёв от назначенной руками.

ALTER TABLE topologies
    ADD COLUMN IF NOT EXISTS auto boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN topologies.auto IS
    'true — платформа сама разложила слои по узлам, которые сообщили, что умеют шард.';
