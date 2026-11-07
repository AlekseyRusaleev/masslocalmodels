package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LayerCount — сколько блоков у модели, которую платформа умеет резать.
//
// Ноль означает «не режем»: лучше оставить реплику на одном узле, чем
// выдумать число слоёв и раздать пустые диапазоны.
func LayerCount(model string) int {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-oss:20b", "gpt-oss:20b:latest":
		return 24
	default:
		return 0
	}
}

// SplitLayers делит слои 0..total-1 на parts непрерывных кусков.
//
// Лишние слои получают первые куски, поэтому диапазоны отличаются не больше
// чем на один слой. Кусков не больше, чем слоёв.
func SplitLayers(total, parts int) [][2]int {
	if total <= 0 || parts <= 0 {
		return nil
	}
	if parts > total {
		parts = total
	}
	base := total / parts
	extra := total % parts
	out := make([][2]int, 0, parts)
	start := 0
	for index := 0; index < parts; index++ {
		count := base
		if index < extra {
			count++
		}
		out = append(out, [2]int{start, start + count - 1})
		start += count
	}
	return out
}

// EnsureLayerSplit держит автоматическую раскладку слоёв в актуальном виде.
//
// Обычный путь — группа слоёв. Узел, который сам сообщил model_fits, в группу
// не входит и считает свои запросы целиком. Остальные узлы от этого не
// распускаются. Меньше двух участников группы снимает только автоматическую
// раскладку. Ручная топология не трогается.
func (s *Store) EnsureLayerSplit(ctx context.Context, model string) error {
	layers := LayerCount(model)
	if layers == 0 {
		return nil
	}
	model = strings.TrimSpace(model)

	var revisionID string
	err := s.pool.QueryRow(ctx, `
		SELECT r.id::text
		FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		WHERE lower(m.name) = lower($1) AND r.status = 'active'
		ORDER BY r.created_at DESC
		LIMIT 1`, model).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: revision for split: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (h.worker_id) h.worker_id::text
		FROM worker_heartbeats h
		JOIN worker_nodes w ON w.id = h.worker_id
		WHERE h.observed_at > now() - interval '2 minutes'
		  AND w.state = 'available'
		  AND h.meta_json->>'mesh_capable' = 'true'
		  AND coalesce(h.meta_json->>'model_fits', '') <> 'true'
		  AND lower(h.meta_json->>'mesh_model') = lower($1)
		ORDER BY h.worker_id, h.observed_at DESC`, model)
	if err != nil {
		return fmt.Errorf("store: mesh workers: %w", err)
	}
	defer rows.Close()

	var workers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("store: scan mesh worker: %w", err)
		}
		workers = append(workers, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: mesh workers: %w", err)
	}
	sort.Strings(workers)

	activeWorkers, auto, found, err := s.activeSplitWorkers(ctx, revisionID)
	if err != nil {
		return err
	}
	if len(workers) < 2 {
		if found && auto {
			return s.drainAutoSplit(ctx, revisionID)
		}
		return nil
	}
	if found && auto && sameIDs(activeWorkers, workers) {
		return nil
	}

	ranges := SplitLayers(layers, len(workers))
	stages := make([]StageInput, len(workers))
	for index, id := range workers {
		role := "compute"
		if index == 0 {
			role = "coordinate"
		}
		width := ranges[index][1] - ranges[index][0] + 1
		stages[index] = StageInput{
			WorkerID:   id,
			LayerStart: ranges[index][0],
			LayerEnd:   ranges[index][1],
			Share:      float32(width) / float32(layers),
			Role:       role,
		}
	}
	detail, err := s.CreateTopology(ctx, revisionID, workers[0], "layer", stages)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE topologies SET auto = true WHERE id = $1::uuid`, detail.Topology.ID); err != nil {
		return fmt.Errorf("store: mark auto topology: %w", err)
	}
	return s.SetTopologyStatus(ctx, detail.Topology.ID, "active")
}

func (s *Store) activeSplitWorkers(ctx context.Context, revisionID string) ([]string, bool, bool, error) {
	var auto bool
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, auto FROM topologies
		WHERE revision_id = $1::uuid AND status = 'active'
		LIMIT 1`, revisionID).Scan(&id, &auto)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("store: active split: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT worker_id::text FROM topology_stages
		WHERE topology_id = $1::uuid ORDER BY worker_id`, id)
	if err != nil {
		return nil, false, false, fmt.Errorf("store: active stages: %w", err)
	}
	defer rows.Close()
	var workers []string
	for rows.Next() {
		var workerID string
		if err := rows.Scan(&workerID); err != nil {
			return nil, false, false, fmt.Errorf("store: scan stage: %w", err)
		}
		workers = append(workers, workerID)
	}
	return workers, auto, true, rows.Err()
}

func (s *Store) drainAutoSplit(ctx context.Context, revisionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE topologies SET status = 'draining'
		WHERE revision_id = $1::uuid AND status = 'active' AND auto`, revisionID)
	if err != nil {
		return fmt.Errorf("store: drain auto split: %w", err)
	}
	return nil
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
