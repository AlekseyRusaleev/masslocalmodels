package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Seeder — включённый компьютер, который может хранить куски модели.
type Seeder struct {
	ID            string
	DiskFreeBytes *int64
	Held          []string
}

// ActiveWeightCatalog возвращает свежую активную ревизию.
// skipModel исключает снятую тестовую модель, чтобы её файлы больше не назначались.
func (s *Store) ActiveWeightCatalog(ctx context.Context, skipModel string) (Manifest, error) {
	const q = `
		SELECT r.id::text
		FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		WHERE r.status = 'active' AND lower(m.name) <> lower($1)
		ORDER BY r.created_at DESC
		LIMIT 1`

	var revisionID string
	err := s.pool.QueryRow(ctx, q, skipModel).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrNotFound
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("store: active weights: %w", err)
	}
	return s.ManifestByRevision(ctx, revisionID)
}

// OnlineSeeders — компьютеры, которые прямо сейчас в сети.
// Held читается из последнего heartbeat: агент сам перечисляет файлы на диске.
func (s *Store) OnlineSeeders(ctx context.Context, within time.Duration) ([]Seeder, error) {
	const q = `
		SELECT w.id::text, h.disk_free_bytes,
		       COALESCE(h.meta_json->'held_pieces', '[]'::jsonb)
		FROM worker_nodes w
		LEFT JOIN LATERAL (
		    SELECT disk_free_bytes, meta_json
		    FROM worker_heartbeats hb
		    WHERE hb.worker_id = w.id
		    ORDER BY observed_at DESC
		    LIMIT 1
		) h ON true
		WHERE w.state = 'available'
		  AND COALESCE(w.last_seen_at, w.enrolled_at) > now() - make_interval(secs => $1)
		ORDER BY w.id`

	rows, err := s.pool.Query(ctx, q, int(within.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("store: online seeders: %w", err)
	}
	defer rows.Close()

	out := make([]Seeder, 0, 8)
	for rows.Next() {
		var item Seeder
		var raw []byte
		if err := rows.Scan(&item.ID, &item.DiskFreeBytes, &raw); err != nil {
			return nil, fmt.Errorf("store: scan seeder: %w", err)
		}
		var names []string
		if len(raw) > 0 && json.Unmarshal(raw, &names) == nil {
			item.Held = names
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
