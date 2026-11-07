-- 0020_rag.sql — база знаний: документы, чанки и поиск по векторам.
--
-- Зачем: эмбеддинги уже считаются на узлах, но хранить и искать их было негде.
-- Теперь документ режется на чанки, каждый получает вектор, и поиск идёт по
-- косинусной близости.
--
-- Почему не pgvector: расширения на сервере нет (доступен только pg_trgm).
-- На масштабе личной сети (сотни-тысячи чанков) честный перебор с расчётом
-- близости в SQL работает достаточно быстро, а зависимость не нужна. Когда
-- чанков станут десятки тысяч, сюда встанет pgvector без смены схемы: поле
-- станет vector(768).

CREATE TABLE IF NOT EXISTS documents (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title       text        NOT NULL DEFAULT '',
    source      text        NOT NULL DEFAULT '',
    chunk_count integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS documents_user_idx ON documents (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS document_chunks (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    ordinal     integer     NOT NULL,
    content     text        NOT NULL,
    embedding   double precision[] NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT document_chunks_embedding_check CHECK (array_length(embedding, 1) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS document_chunks_order_uniq
    ON document_chunks (document_id, ordinal);

COMMENT ON TABLE documents IS
    'Документ базы знаний: принадлежит аккаунту, виден только ему.';
COMMENT ON COLUMN document_chunks.embedding IS
    'Вектор чанка. Считается на узле сети и хранится массивом: pgvector на сервере нет.';

-- Косинусная близость двух векторов.
--
-- Одна функция на всё приложение: поиск, проверки и будущие пороги используют
-- одно и то же определение, поэтому оценки сравнимы между собой.
CREATE OR REPLACE FUNCTION cosine_similarity(a double precision[], b double precision[])
RETURNS double precision AS $$
    SELECT CASE
             WHEN s.dot = 0 OR s.len_a = 0 OR s.len_b = 0 THEN 0
             ELSE s.dot / (sqrt(s.len_a) * sqrt(s.len_b))
           END
    FROM (
        SELECT sum(x * y) AS dot, sum(x * x) AS len_a, sum(y * y) AS len_b
        FROM unnest(a, b) AS pair(x, y)
    ) AS s;
$$ LANGUAGE sql IMMUTABLE STRICT;
