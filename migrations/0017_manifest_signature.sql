-- 0017_manifest_signature.sql — подпись манифеста и причина аттестации.
--
-- Зачем подпись: каталог ревизий сейчас защищён только доступом к API. Если
-- кто-то получит доступ к базе или к административному ключу, он подменит
-- sha256 в манифесте — и аттестация начнёт подтверждать чужие веса. Подпись
-- ключом платформы отделяет «манифест записан» от «манифест закреплён
-- администратором и с тех пор не менялся».
--
-- Зачем причина: панель должна отличать «не проверяли» от «манифеста нет» и от
-- «манифест не подписан». Иначе «не подтверждено» выглядит одинаково во всех
-- случаях и ничего не объясняет.

ALTER TABLE model_revisions
    ADD COLUMN IF NOT EXISTS manifest_signature text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS manifest_signed_at timestamptz;

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS attestation text NOT NULL DEFAULT '';

ALTER TABLE worker_model_digests
    ADD COLUMN IF NOT EXISTS self_verified boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN model_revisions.manifest_signature IS
    'HMAC-SHA256(pepper, канонический текст манифеста). Пусто — манифест не закреплён.';
COMMENT ON COLUMN jobs.attestation IS
    'Итог сверки весов: verified, no_manifest, unsigned_manifest, mismatch, not_reported.';
COMMENT ON COLUMN worker_model_digests.self_verified IS
    'Узел сам пересчитал sha256 файла весов и сверил с digest — не поверил рантайму на слово.';
