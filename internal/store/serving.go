package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServingModel — модель, которую прямо сейчас обслуживает хотя бы один узел.
//
// Источник данных — колонка worker_nodes.served_model, которую агент обновляет
// каждым heartbeat. Это единственный честный ответ на вопрос «что можно
// вызвать»: каталог может быть пустым, а узел при этом уже держит модель.
type ServingModel struct {
	ID       string    `json:"id"`
	Object   string    `json:"object"`
	OwnedBy  string    `json:"owned_by"`
	ServedBy []string  `json:"served_by"`
	LastSeen time.Time `json:"last_seen"`
	// Created — Unix-время последней заявки модели. Поле есть в ответе OpenAI,
	// и клиенты (SDK, редакторы) его ожидают.
	Created int64 `json:"created"`
}

// ServingModels возвращает модели, которые обслуживают узлы, подававшие признаки
// жизни за последние `within`. Узлы в паузе, drain и карантине не учитываются.
func (s *Store) ServingModels(ctx context.Context, within time.Duration) ([]ServingModel, error) {
	const q = `
		SELECT served_model,
		       array_agg(name ORDER BY name),
		       max(COALESCE(last_seen_at, enrolled_at))
		FROM worker_nodes
		WHERE state = 'available'
		  AND served_model <> ''
		  AND COALESCE(last_seen_at, enrolled_at) > now() - make_interval(secs => $1)
		GROUP BY served_model
		ORDER BY served_model`

	rows, err := s.pool.Query(ctx, q, int(within.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("store: serving models: %w", err)
	}
	defer rows.Close()

	out := make([]ServingModel, 0, 4)
	for rows.Next() {
		var m ServingModel
		if err := rows.Scan(&m.ID, &m.ServedBy, &m.LastSeen); err != nil {
			return nil, fmt.Errorf("store: scan serving model: %w", err)
		}
		m.Object = "model"
		m.OwnedBy = "worker"
		m.Created = m.LastSeen.Unix()
		out = append(out, m)
	}
	return out, rows.Err()
}

// ServingNodes возвращает узлы, которые заявляют указанную модель.
// Пустой список означает «вызывать некуда» — API отвечает понятной ошибкой,
// а не ждёт ответа десять минут.
func (s *Store) ServingNodes(ctx context.Context, model string, within time.Duration) ([]string, error) {
	return s.servingNodesBy(ctx, "served_model", model, within, "")
}

// ServingEmbeddingNodes — узлы, которые считают эмбеддинги указанной моделью.
func (s *Store) ServingEmbeddingNodes(ctx context.Context, model string, within time.Duration) ([]string, error) {
	return s.servingNodesBy(ctx, "served_embedding_model", model, within, "")
}

// servingNodesBy — общий запрос: колонка выбирается из двух известных значений.
// ownerID ограничивает выборку машинами одного человека. Пустой — вся сеть.
func (s *Store) servingNodesBy(ctx context.Context, column, model string, within time.Duration, ownerID string) ([]string, error) {
	q := `
		SELECT array_agg(name ORDER BY name)
		FROM worker_nodes
		WHERE state = 'available'
		  AND ` + column + ` = $2
		  AND COALESCE(last_seen_at, enrolled_at) > now() - make_interval(secs => $1)`
	args := []any{int(within.Seconds()), model}
	if ownerID != "" {
		q += ` AND owner_user_id = $3::uuid`
		args = append(args, ownerID)
	}

	var nodes []string
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&nodes); err != nil {
		// NULL от array_agg означает «никто не заявил модель» — это не ошибка БД.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: serving nodes: %w", err)
	}
	return nodes, nil
}

// ServingNodesForOwner — узлы этого человека, которые держат модель.
func (s *Store) ServingNodesForOwner(ctx context.Context, model, ownerID string, within time.Duration) ([]string, error) {
	return s.servingNodesBy(ctx, "served_model", model, within, ownerID)
}

// ServingEmbeddingNodesForOwner — узлы этого человека, которые считают эмбеддинги.
func (s *Store) ServingEmbeddingNodesForOwner(ctx context.Context, model, ownerID string, within time.Duration) ([]string, error) {
	return s.servingNodesBy(ctx, "served_embedding_model", model, within, ownerID)
}
