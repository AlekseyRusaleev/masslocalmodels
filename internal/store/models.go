package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Model — логическая модель каталога.
type Model struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	TaskType    string    `json:"task_type"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// Revision — закреплённая ревизия модели с лицензией.
type Revision struct {
	ID             string  `json:"id"`
	ModelID        string  `json:"model_id"`
	ModelName      string  `json:"model_name,omitempty"`
	Source         string  `json:"source"`
	RevisionRef    string  `json:"revision"`
	License        string  `json:"license"`
	LicenseChecked bool    `json:"license_checked"`
	Runtime        string  `json:"runtime"`
	Quantization   string  `json:"quantization"`
	ParamsBillions float32 `json:"params_billions,omitempty"`
	SizeBytes      int64   `json:"size_bytes"`
	// ManifestSignature — HMAC подписи манифеста. Пусто — не подписан.
	ManifestSignature string     `json:"manifest_signature,omitempty"`
	ManifestSignedAt  *time.Time `json:"manifest_signed_at,omitempty"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`

	// ArtifactCount — сколько файлов уже зарегистрировано. Ревизия без файлов
	// не готова к раздаче, и панель должна показывать это явно.
	ArtifactCount int `json:"artifact_count"`
}

// Artifact — файл ревизии, который узел скачивает сам.
// LayerStart/LayerEnd = nil означает «общий файл» (конфиг, токенизатор).
type Artifact struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Size       int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	Ordinal    int    `json:"ordinal"`
	LayerStart *int   `json:"layer_start,omitempty"`
	LayerEnd   *int   `json:"layer_end,omitempty"`
}

// Manifest — то, что получает узел: ревизия, назначение и нужные ему файлы.
type Manifest struct {
	Revision   Revision   `json:"revision"`
	Assignment *Stage     `json:"assignment,omitempty"`
	Artifacts  []Artifact `json:"artifacts"`
}

// EnsureModel создаёт или возвращает модель по логическому имени.
func (s *Store) EnsureModel(ctx context.Context, name, displayName, taskType string) (Model, error) {
	const q = `
		INSERT INTO model_catalog (name, display_name, task_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (lower(name)) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id::text, name, display_name, task_type, status, created_at`

	var m Model
	if err := s.pool.QueryRow(ctx, q, name, displayName, taskType).Scan(
		&m.ID, &m.Name, &m.DisplayName, &m.TaskType, &m.Status, &m.CreatedAt); err != nil {
		return Model{}, fmt.Errorf("store: ensure model: %w", err)
	}
	return m, nil
}

// CreateRevision добавляет ревизию к модели.
func (s *Store) CreateRevision(ctx context.Context, modelID, source, revisionRef, license string, licenseChecked bool, runtime, quantization string, paramsBillions float32) (Revision, error) {
	const q = `
		INSERT INTO model_revisions (model_id, source, revision, license, license_checked, runtime, quantization, params_billions)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, NULLIF($8, 0))
		RETURNING id::text, model_id::text, source, revision, license, license_checked, runtime, quantization,
		          COALESCE(params_billions, 0), size_bytes, status, created_at`

	var r Revision
	err := s.pool.QueryRow(ctx, q, modelID, source, revisionRef, license, licenseChecked, runtime, quantization, paramsBillions).Scan(
		&r.ID, &r.ModelID, &r.Source, &r.RevisionRef, &r.License, &r.LicenseChecked,
		&r.Runtime, &r.Quantization, &r.ParamsBillions, &r.SizeBytes, &r.Status, &r.CreatedAt)
	if err != nil {
		return Revision{}, fmt.Errorf("store: create revision: %w", err)
	}
	return r, nil
}

// AddArtifact прикрепляет файл к ревизии и увеличивает суммарный размер.
// layerStart/layerEnd могут быть nil — тогда файл считается общим.
func (s *Store) AddArtifact(ctx context.Context, revisionID, name, url, sha256 string, size int64, ordinal int, layerStart, layerEnd *int) (Artifact, error) {
	const q = `
		INSERT INTO model_artifacts (revision_id, name, url, size_bytes, sha256, ordinal, layer_start, layer_end)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (revision_id, name) DO UPDATE
		    SET url = EXCLUDED.url, size_bytes = EXCLUDED.size_bytes,
		        sha256 = EXCLUDED.sha256, ordinal = EXCLUDED.ordinal,
		        layer_start = EXCLUDED.layer_start, layer_end = EXCLUDED.layer_end
		RETURNING id::text, name, url, size_bytes, sha256, ordinal, layer_start, layer_end`

	var a Artifact
	if err := s.pool.QueryRow(ctx, q, revisionID, name, url, size, sha256, ordinal, layerStart, layerEnd).Scan(
		&a.ID, &a.Name, &a.URL, &a.Size, &a.SHA256, &a.Ordinal, &a.LayerStart, &a.LayerEnd); err != nil {
		return Artifact{}, fmt.Errorf("store: add artifact: %w", err)
	}

	const recalc = `
		UPDATE model_revisions r SET size_bytes = COALESCE((
		    SELECT SUM(size_bytes) FROM model_artifacts WHERE revision_id = r.id
		), 0) WHERE r.id = $1::uuid`
	if _, err := s.pool.Exec(ctx, recalc, revisionID); err != nil {
		return Artifact{}, fmt.Errorf("store: recalc size: %w", err)
	}
	return a, nil
}

// ManifestByRevision отдаёт ревизию и все её артефакты (для администратора).
func (s *Store) ManifestByRevision(ctx context.Context, revisionID string) (Manifest, error) {
	return s.manifest(ctx, revisionID, nil)
}

// ManifestForModel находит манифест модели по её имени в каталоге.
//
// Берётся самая свежая ревизия в состоянии ready или active: черновик ещё не
// раздаётся, выведенная — уже нет, и сверять веса по ним нельзя.
func (s *Store) ManifestForModel(ctx context.Context, model string) (Manifest, error) {
	const q = `
		SELECT r.id::text FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		WHERE lower(m.name) = lower($1) AND r.status IN ('ready', 'active')
		ORDER BY r.created_at DESC
		LIMIT 1`

	var revisionID string
	err := s.pool.QueryRow(ctx, q, model).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrNotFound
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("store: manifest for model: %w", err)
	}
	return s.ManifestByRevision(ctx, revisionID)
}

// ManifestForWorker отдаёт узлу только те артефакты, которые пересекаются с его
// этапом в активной топологии, плюс общие файлы без диапазона слоёв.
func (s *Store) ManifestForWorker(ctx context.Context, revisionID, workerID string) (Manifest, error) {
	stage, err := s.stageForWorker(ctx, revisionID, workerID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Manifest{}, err
	}
	return s.manifest(ctx, revisionID, stage)
}

// stageForWorker ищет активный или готовящийся этап узла в топологии ревизии.
func (s *Store) stageForWorker(ctx context.Context, revisionID, workerID string) (*Stage, error) {
	const q = `
		SELECT st.id::text, st.stage_index, st.worker_id::text, w.name,
		       st.layer_start, st.layer_end, st.share, st.role
		FROM topology_stages st
		JOIN topologies t ON t.id = st.topology_id
		JOIN worker_nodes w ON w.id = st.worker_id
		WHERE t.revision_id = $1::uuid AND st.worker_id = $2::uuid
		  AND t.status IN ('active', 'staging')
		ORDER BY t.version DESC LIMIT 1`

	var st Stage
	err := s.pool.QueryRow(ctx, q, revisionID, workerID).Scan(&st.ID, &st.StageIndex, &st.WorkerID,
		&st.WorkerName, &st.LayerStart, &st.LayerEnd, &st.Share, &st.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: stage for worker: %w", err)
	}
	return &st, nil
}

// manifest собирает ревизию и артефакты; stage == nil — все артефакты.
func (s *Store) manifest(ctx context.Context, revisionID string, stage *Stage) (Manifest, error) {
	const revQ = `
		SELECT r.id::text, r.model_id::text, m.name, r.source, r.revision, r.license,
		       r.license_checked, r.runtime, r.quantization, COALESCE(r.params_billions, 0), r.size_bytes, r.status, r.created_at,
		       r.manifest_signature, r.manifest_signed_at
		FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		WHERE r.id = $1::uuid`

	var m Manifest
	err := s.pool.QueryRow(ctx, revQ, revisionID).Scan(
		&m.Revision.ID, &m.Revision.ModelID, &m.Revision.ModelName, &m.Revision.Source,
		&m.Revision.RevisionRef, &m.Revision.License, &m.Revision.LicenseChecked,
		&m.Revision.Runtime, &m.Revision.Quantization, &m.Revision.ParamsBillions,
		&m.Revision.SizeBytes, &m.Revision.Status, &m.Revision.CreatedAt,
		&m.Revision.ManifestSignature, &m.Revision.ManifestSignedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrNotFound
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("store: revision: %w", err)
	}
	m.Assignment = stage

	const artQ = `
		SELECT id::text, name, url, size_bytes, sha256, ordinal, layer_start, layer_end
		FROM model_artifacts WHERE revision_id = $1::uuid ORDER BY ordinal, name`

	rows, err := s.pool.Query(ctx, artQ, revisionID)
	if err != nil {
		return Manifest{}, fmt.Errorf("store: artifacts: %w", err)
	}
	defer rows.Close()

	m.Artifacts = make([]Artifact, 0, 4)
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.Name, &a.URL, &a.Size, &a.SHA256, &a.Ordinal,
			&a.LayerStart, &a.LayerEnd); err != nil {
			return Manifest{}, fmt.Errorf("store: scan artifact: %w", err)
		}
		if stage != nil && !artifactNeededByStage(a, *stage) {
			continue
		}
		m.Artifacts = append(m.Artifacts, a)
	}
	return m, rows.Err()
}

// artifactNeededByStage решает, нужен ли файл узлу:
// общие файлы (без диапазона) нужны всегда, слоёные — только при пересечении.
func artifactNeededByStage(a Artifact, stage Stage) bool {
	if a.LayerStart == nil || a.LayerEnd == nil {
		return true
	}
	return *a.LayerStart <= stage.LayerEnd && *a.LayerEnd >= stage.LayerStart
}

// SetRevisionStatus меняет статус ревизии (draft → ready → active → retired).
func (s *Store) SetRevisionStatus(ctx context.Context, revisionID, status string) error {
	const q = `UPDATE model_revisions SET status = $2 WHERE id = $1::uuid`
	tag, err := s.pool.Exec(ctx, q, revisionID, status)
	if err != nil {
		return fmt.Errorf("store: set revision status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRevisions выдаёт каталог ревизий с числом артефактов.
func (s *Store) ListRevisions(ctx context.Context) ([]Revision, error) {
	const q = `
		SELECT r.id::text, r.model_id::text, m.name, r.source, r.revision, r.license,
		       r.license_checked, r.runtime, r.quantization, COALESCE(r.params_billions, 0), r.size_bytes,
		       r.status, r.created_at, r.manifest_signature, r.manifest_signed_at,
		       (SELECT count(*) FROM model_artifacts a WHERE a.revision_id = r.id)
		FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		ORDER BY r.created_at DESC`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list revisions: %w", err)
	}
	defer rows.Close()

	out := make([]Revision, 0, 8)
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.ID, &r.ModelID, &r.ModelName, &r.Source, &r.RevisionRef,
			&r.License, &r.LicenseChecked, &r.Runtime, &r.Quantization, &r.ParamsBillions,
			&r.SizeBytes, &r.Status, &r.CreatedAt, &r.ManifestSignature, &r.ManifestSignedAt, &r.ArtifactCount); err != nil {
			return nil, fmt.Errorf("store: scan revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
