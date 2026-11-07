-- 0005_topologies.sql — топологии: какие узлы держат какие слои ревизии.
--
-- Соответствует плану §6.3 (перераспределение весов) и §19.2: топология
-- версионируется, старые не мутируются на месте, переход — через staging.
-- Узел по топологии узнаёт свой диапазон слоёв и качает только его.

CREATE TABLE IF NOT EXISTS topologies (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id            uuid        NOT NULL REFERENCES model_revisions (id) ON DELETE CASCADE,
    version                integer     NOT NULL,
    status                 text        NOT NULL DEFAULT 'draft',
    coordinator_worker_id  uuid        REFERENCES worker_nodes (id) ON DELETE SET NULL,
    split_mode             text        NOT NULL DEFAULT 'layer',
    created_at             timestamptz NOT NULL DEFAULT now(),
    activated_at           timestamptz,
    CONSTRAINT topologies_status_check CHECK (status IN
        ('draft', 'staging', 'active', 'draining', 'retired', 'failed')),
    CONSTRAINT topologies_split_check CHECK (split_mode IN ('layer', 'row', 'tensor'))
);

CREATE UNIQUE INDEX IF NOT EXISTS topologies_revision_version_uniq
    ON topologies (revision_id, version);
CREATE INDEX IF NOT EXISTS topologies_status_idx ON topologies (status, revision_id);

CREATE TABLE IF NOT EXISTS topology_stages (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    topology_id uuid        NOT NULL REFERENCES topologies (id) ON DELETE CASCADE,
    stage_index integer     NOT NULL,
    worker_id   uuid        NOT NULL REFERENCES worker_nodes (id) ON DELETE CASCADE,
    layer_start integer     NOT NULL DEFAULT 0,
    layer_end   integer     NOT NULL DEFAULT 0,
    share       real        NOT NULL DEFAULT 0,
    role        text        NOT NULL DEFAULT 'compute',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT topology_stages_role_check CHECK (role IN ('compute', 'coordinate')),
    CONSTRAINT topology_stages_layers_check CHECK (layer_end >= layer_start)
);

CREATE UNIQUE INDEX IF NOT EXISTS topology_stages_index_uniq
    ON topology_stages (topology_id, stage_index);
CREATE INDEX IF NOT EXISTS topology_stages_worker_idx ON topology_stages (worker_id);

COMMENT ON TABLE topologies IS
    'Версия размещения ревизии. Активная топология одна на ревизию, смена — через staging и drain.';
COMMENT ON COLUMN topology_stages.layer_start IS
    'Первый слой блока (включительно), который держит узел. Узел качает только эти веса.';
