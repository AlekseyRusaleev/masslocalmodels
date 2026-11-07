package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Document — документ базы знаний, принадлежащий аккаунту.
type Document struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Title      string    `json:"title"`
	Source     string    `json:"source"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// Chunk — часть документа с вектором.
type Chunk struct {
	Ordinal   int       `json:"ordinal"`
	Content   string    `json:"content"`
	Embedding []float64 `json:"-"`
}

// SearchHit — найденный чанк и его близость к запросу.
type SearchHit struct {
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	Ordinal       int     `json:"ordinal"`
	Content       string  `json:"content"`
	Similarity    float64 `json:"similarity"`
}

// CreateDocument сохраняет документ вместе с чанками и их векторами.
//
// Всё одной транзакцией: документ без чанков или чанки без документа — это
// мусор в базе знаний, который потом невозможно ни показать, ни удалить.
func (s *Store) CreateDocument(ctx context.Context, userID, title, source string, chunks []Chunk) (Document, error) {
	if len(chunks) == 0 {
		return Document{}, errors.New("store: document has no chunks")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Document{}, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertDoc = `
		INSERT INTO documents (user_id, title, source, chunk_count)
		VALUES ($1::uuid, $2, $3, $4)
		RETURNING id::text, user_id::text, title, source, chunk_count, created_at`

	var document Document
	if err := tx.QueryRow(ctx, insertDoc, userID, title, source, len(chunks)).Scan(
		&document.ID, &document.UserID, &document.Title, &document.Source,
		&document.ChunkCount, &document.CreatedAt); err != nil {
		return Document{}, fmt.Errorf("store: insert document: %w", err)
	}

	const insertChunk = `
		INSERT INTO document_chunks (document_id, ordinal, content, embedding)
		VALUES ($1::uuid, $2, $3, $4)`
	for index, chunk := range chunks {
		if _, err := tx.Exec(ctx, insertChunk, document.ID, index, chunk.Content, chunk.Embedding); err != nil {
			return Document{}, fmt.Errorf("store: insert chunk: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Document{}, fmt.Errorf("store: commit: %w", err)
	}
	return document, nil
}

// ListDocuments перечисляет документы аккаунта, свежие первыми.
func (s *Store) ListDocuments(ctx context.Context, userID string, limit int) ([]Document, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id::text, user_id::text, title, source, chunk_count, created_at
		FROM documents
		WHERE user_id = $1::uuid
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := s.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list documents: %w", err)
	}
	defer rows.Close()

	out := make([]Document, 0, 8)
	for rows.Next() {
		var document Document
		if err := rows.Scan(&document.ID, &document.UserID, &document.Title, &document.Source,
			&document.ChunkCount, &document.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan document: %w", err)
		}
		out = append(out, document)
	}
	return out, rows.Err()
}

// DeleteDocument удаляет документ вместе с чанками.
func (s *Store) DeleteDocument(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM documents WHERE id = $2::uuid AND user_id = $1::uuid`, userID, id)
	if err != nil {
		return fmt.Errorf("store: delete document: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchChunks ищет ближайшие чанки аккаунта по косинусной близости.
//
// Считаем в базе: чанков немного, а гонять их в сервис ради перемножения
// векторов дороже, чем один SQL-запрос с сортировкой.
func (s *Store) SearchChunks(ctx context.Context, userID string, query []float64, limit int) ([]SearchHit, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	const q = `
		SELECT c.document_id::text, d.title, c.ordinal, c.content,
		       cosine_similarity(c.embedding, $2) AS similarity
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.user_id = $1::uuid
		ORDER BY similarity DESC
		LIMIT $3`

	rows, err := s.pool.Query(ctx, q, userID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search chunks: %w", err)
	}
	defer rows.Close()

	out := make([]SearchHit, 0, limit)
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.DocumentID, &hit.DocumentTitle, &hit.Ordinal, &hit.Content,
			&hit.Similarity); err != nil {
			return nil, fmt.Errorf("store: scan search hit: %w", err)
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

// DocumentByID возвращает документ, только если он принадлежит аккаунту.
func (s *Store) DocumentByID(ctx context.Context, userID, id string) (Document, error) {
	const q = `
		SELECT id::text, user_id::text, title, source, chunk_count, created_at
		FROM documents
		WHERE id = $2::uuid AND user_id = $1::uuid`

	var document Document
	err := s.pool.QueryRow(ctx, q, userID, id).Scan(&document.ID, &document.UserID,
		&document.Title, &document.Source, &document.ChunkCount, &document.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, fmt.Errorf("store: document by id: %w", err)
	}
	return document, nil
}

// EmbeddingModel выбирает модель эмбеддингов из тех, что объявили живущие узлы.
//
// Клиенту не нужно знать имена моделей: платформа берёт ту, что реально
// обслуживается. Если ни одна не объявлена — честная ошибка, а не пустой вектор.
func (s *Store) EmbeddingModel(ctx context.Context, within time.Duration) (string, error) {
	const q = `
		SELECT served_embedding_model
		FROM worker_nodes
		WHERE state = 'available'
		  AND served_embedding_model <> ''
		  AND COALESCE(last_seen_at, enrolled_at) > now() - make_interval(secs => $1)
		ORDER BY COALESCE(last_seen_at, enrolled_at) DESC
		LIMIT 1`

	var model string
	err := s.pool.QueryRow(ctx, q, int(within.Seconds())).Scan(&model)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: embedding model: %w", err)
	}
	return model, nil
}
