package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Topology — версия размещения ревизии по узлам.
type Topology struct {
	ID            string  `json:"id"`
	RevisionID    string  `json:"revision_id"`
	ModelName     string  `json:"model_name,omitempty"`
	Version       int     `json:"version"`
	Status        string  `json:"status"`
	CoordinatorID *string `json:"coordinator_worker_id,omitempty"`
	SplitMode     string  `json:"split_mode"`
	// Auto — группу собрала платформа. В такой группе ведущий — компьютер
	// владельца чата, чужие машины только держат слои. Ручная группа этого
	// свойства не имеет.
	Auto bool `json:"auto,omitempty"`
	// Error — почему группа не может подняться. Пусто у рабочей группы.
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	TotalLayers int        `json:"total_layers"`
}

// Stage — один узел в топологии со своим диапазоном слоёв.
type Stage struct {
	ID         string `json:"id"`
	StageIndex int    `json:"stage_index"`
	WorkerID   string `json:"worker_id"`
	WorkerName string `json:"worker_name,omitempty"`
	// RPCAddr — адрес ggml-rpc-server этого узла в шард-группе. Пусто
	// означает, что узел ещё не объявил адрес: координатор не сможет к нему
	// подключиться, и об этом нужно сказать явно, а не молча падать.
	RPCAddr    string  `json:"rpc_addr,omitempty"`
	LayerStart int     `json:"layer_start"`
	LayerEnd   int     `json:"layer_end"`
	Share      float32 `json:"share"`
	Role       string  `json:"role"`
}

// StageInput — данные этапа из запроса администратора.
type StageInput struct {
	WorkerID   string  `json:"worker_id"`
	LayerStart int     `json:"layer_start"`
	LayerEnd   int     `json:"layer_end"`
	Share      float32 `json:"share"`
	Role       string  `json:"role"`
}

// TopologyDetail — топология вместе с этапами.
type TopologyDetail struct {
	Topology Topology `json:"topology"`
	Stages   []Stage  `json:"stages"`
}

// ErrConflict возвращается при нарушении уникальности или состояния.
var ErrConflict = errors.New("store: conflict")

// CreateTopology создаёт новую версию топологии и её этапы в одной транзакции.
func (s *Store) CreateTopology(ctx context.Context, revisionID, coordinatorID, splitMode string, stages []StageInput) (TopologyDetail, error) {
	if splitMode == "" {
		splitMode = "layer"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TopologyDetail{}, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var nextVersion int
	const verQ = `SELECT COALESCE(MAX(version), 0) + 1 FROM topologies WHERE revision_id = $1::uuid`
	if err := tx.QueryRow(ctx, verQ, revisionID).Scan(&nextVersion); err != nil {
		return TopologyDetail{}, fmt.Errorf("store: next version: %w", err)
	}

	var t Topology
	const insT = `
		INSERT INTO topologies (revision_id, version, coordinator_worker_id, split_mode)
		VALUES ($1::uuid, $2, NULLIF($3, '')::uuid, $4)
		RETURNING id::text, revision_id::text, version, status, coordinator_worker_id::text,
		          split_mode, error, created_at, activated_at`
	if err := tx.QueryRow(ctx, insT, revisionID, nextVersion, coordinatorID, splitMode).Scan(
		&t.ID, &t.RevisionID, &t.Version, &t.Status, &t.CoordinatorID, &t.SplitMode,
		&t.Error, &t.CreatedAt, &t.ActivatedAt); err != nil {
		return TopologyDetail{}, fmt.Errorf("store: insert topology: %w", err)
	}

	detail := TopologyDetail{Topology: t, Stages: make([]Stage, 0, len(stages))}
	const insS = `
		INSERT INTO topology_stages (topology_id, stage_index, worker_id, layer_start, layer_end, share, role)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7)
		RETURNING id::text, stage_index, worker_id::text, layer_start, layer_end, share, role`

	for index, stage := range stages {
		role := stage.Role
		if role == "" {
			role = "compute"
		}
		var saved Stage
		if err := tx.QueryRow(ctx, insS, t.ID, index, stage.WorkerID, stage.LayerStart,
			stage.LayerEnd, stage.Share, role).Scan(&saved.ID, &saved.StageIndex, &saved.WorkerID,
			&saved.LayerStart, &saved.LayerEnd, &saved.Share, &saved.Role); err != nil {
			return TopologyDetail{}, fmt.Errorf("store: insert stage: %w", err)
		}
		detail.Stages = append(detail.Stages, saved)
	}

	if err := tx.Commit(ctx); err != nil {
		return TopologyDetail{}, fmt.Errorf("store: commit: %w", err)
	}
	return detail, nil
}

// SetTopologyStatus переводит топологию в новое состояние.
// При активации предыдущая активная топология той же ревизии уходит в draining.
func (s *Store) SetTopologyStatus(ctx context.Context, topologyID, status string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var revisionID string
	const revQ = `SELECT revision_id::text FROM topologies WHERE id = $1::uuid FOR UPDATE`
	if err := tx.QueryRow(ctx, revQ, topologyID).Scan(&revisionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: lock topology: %w", err)
	}

	if status == "active" {
		const drainQ = `
			UPDATE topologies SET status = 'draining'
			WHERE revision_id = $1::uuid AND status = 'active' AND id <> $2::uuid`
		if _, err := tx.Exec(ctx, drainQ, revisionID, topologyID); err != nil {
			return fmt.Errorf("store: drain previous: %w", err)
		}
		const actQ = `
			UPDATE topologies SET status = 'active', activated_at = now() WHERE id = $1::uuid`
		if _, err := tx.Exec(ctx, actQ, topologyID); err != nil {
			return fmt.Errorf("store: activate: %w", err)
		}
	} else {
		const updQ = `UPDATE topologies SET status = $2 WHERE id = $1::uuid`
		if _, err := tx.Exec(ctx, updQ, topologyID, status); err != nil {
			return fmt.Errorf("store: set status: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// TopologyByID возвращает топологию с этапами.
func (s *Store) TopologyByID(ctx context.Context, topologyID string) (TopologyDetail, error) {
	const tQ = `
		SELECT t.id::text, t.revision_id::text, m.name, t.version, t.status,
		       t.coordinator_worker_id::text, t.split_mode, t.auto, t.error, t.created_at, t.activated_at
		FROM topologies t
		JOIN model_revisions r ON r.id = t.revision_id
		JOIN model_catalog m ON m.id = r.model_id
		WHERE t.id = $1::uuid`

	var detail TopologyDetail
	err := s.pool.QueryRow(ctx, tQ, topologyID).Scan(
		&detail.Topology.ID, &detail.Topology.RevisionID, &detail.Topology.ModelName,
		&detail.Topology.Version, &detail.Topology.Status, &detail.Topology.CoordinatorID,
		&detail.Topology.SplitMode, &detail.Topology.Auto, &detail.Topology.Error,
		&detail.Topology.CreatedAt, &detail.Topology.ActivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopologyDetail{}, ErrNotFound
	}
	if err != nil {
		return TopologyDetail{}, fmt.Errorf("store: topology: %w", err)
	}

	stages, err := s.stagesFor(ctx, topologyID)
	if err != nil {
		return TopologyDetail{}, err
	}
	detail.Stages = stages
	for _, stage := range stages {
		if stage.LayerEnd > detail.Topology.TotalLayers {
			detail.Topology.TotalLayers = stage.LayerEnd
		}
	}
	return detail, nil
}

// ActiveTopologyForRevision возвращает активную топологию ревизии.
func (s *Store) ActiveTopologyForRevision(ctx context.Context, revisionID string) (TopologyDetail, error) {
	const q = `SELECT id::text FROM topologies WHERE revision_id = $1::uuid AND status = 'active' LIMIT 1`

	var id string
	err := s.pool.QueryRow(ctx, q, revisionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopologyDetail{}, ErrNotFound
	}
	if err != nil {
		return TopologyDetail{}, fmt.Errorf("store: active topology: %w", err)
	}
	return s.TopologyByID(ctx, id)
}

// ListTopologies выдаёт все топологии с этапами.
func (s *Store) ListTopologies(ctx context.Context) ([]TopologyDetail, error) {
	const q = `
		SELECT id::text FROM topologies ORDER BY created_at DESC LIMIT 100`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list topologies: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, 8)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan topology id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]TopologyDetail, 0, len(ids))
	for _, id := range ids {
		detail, err := s.TopologyByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	return out, nil
}

// stagesFor загружает этапы топологии с именами узлов.
func (s *Store) stagesFor(ctx context.Context, topologyID string) ([]Stage, error) {
	const q = `
		SELECT st.id::text, st.stage_index, st.worker_id::text, w.name, w.mesh_rpc_addr,
		       st.layer_start, st.layer_end, st.share, st.role
		FROM topology_stages st
		JOIN worker_nodes w ON w.id = st.worker_id
		WHERE st.topology_id = $1::uuid
		ORDER BY st.stage_index`

	rows, err := s.pool.Query(ctx, q, topologyID)
	if err != nil {
		return nil, fmt.Errorf("store: stages: %w", err)
	}
	defer rows.Close()

	out := make([]Stage, 0, 4)
	for rows.Next() {
		var st Stage
		if err := rows.Scan(&st.ID, &st.StageIndex, &st.WorkerID, &st.WorkerName, &st.RPCAddr,
			&st.LayerStart, &st.LayerEnd, &st.Share, &st.Role); err != nil {
			return nil, fmt.Errorf("store: scan stage: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// WorkerAssignments возвращает активные этапы конкретного узла.
func (s *Store) WorkerAssignments(ctx context.Context, workerID string) ([]TopologyDetail, error) {
	const q = `
		SELECT DISTINCT t.id::text
		FROM topology_stages st
		JOIN topologies t ON t.id = st.topology_id
		WHERE st.worker_id = $1::uuid AND t.status IN ('active', 'staging')
		ORDER BY 1`

	rows, err := s.pool.Query(ctx, q, workerID)
	if err != nil {
		return nil, fmt.Errorf("store: worker assignments: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan assignment: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]TopologyDetail, 0, len(ids))
	for _, id := range ids {
		detail, err := s.TopologyByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	return out, nil
}

// TopologyOwnerIDs возвращает разных владельцев машин в топологии.
func (s *Store) TopologyOwnerIDs(ctx context.Context, topologyID string) ([]string, error) {
	const ownersQ = `
		SELECT DISTINCT owner_id FROM (
		    SELECT w.owner_user_id::text AS owner_id
		    FROM topology_stages st
		    JOIN worker_nodes w ON w.id = st.worker_id
		    WHERE st.topology_id = $1::uuid
		    UNION
		    SELECT w.owner_user_id::text
		    FROM topologies t
		    JOIN worker_nodes w ON w.id = t.coordinator_worker_id
		    WHERE t.id = $1::uuid AND t.coordinator_worker_id IS NOT NULL
		) owners
		WHERE owner_id IS NOT NULL AND owner_id <> ''
		ORDER BY 1`
	rows, err := s.pool.Query(ctx, ownersQ, topologyID)
	if err != nil {
		return nil, fmt.Errorf("store: topology owners: %w", err)
	}
	defer rows.Close()
	owners := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan topology owner: %w", err)
		}
		owners = append(owners, id)
	}
	return owners, rows.Err()
}
