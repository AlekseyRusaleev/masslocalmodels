// Package store — доступ к PostgreSQL Control Plane.
//
// Содержит только те запросы, которые нужны этапу 1: пользователи и API-ключи.
// Модель данных расширяется миграциями, а не ручными правками схемы.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound возвращается, когда строка отсутствует.
var ErrNotFound = errors.New("store: not found")

// ErrNameTaken — имя узла уже занято другим зарегистрированным узлом.
var ErrNameTaken = errors.New("store: worker name is already taken")

// Store — обёртка над пулом соединений.
type Store struct {
	pool *pgxpool.Pool
}

// User — аккаунт платформы.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// APIKey — метаданные ключа. KeyHash никогда не попадает в JSON.
type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	KeyID      string     `json:"key_id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	KeyHash    []byte     `json:"-"`
	UserStatus string     `json:"-"`
}

// New открывает пул и проверяет соединение.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close освобождает соединения.
func (s *Store) Close() { s.pool.Close() }

// EnsureUser создаёт пользователя или возвращает существующего по email.
func (s *Store) EnsureUser(ctx context.Context, email string) (User, error) {
	const q = `
		INSERT INTO users (email) VALUES ($1)
		ON CONFLICT (lower(email)) DO UPDATE SET email = EXCLUDED.email
		RETURNING id::text, email, status, created_at`

	var u User
	if err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.Status, &u.CreatedAt); err != nil {
		return User{}, fmt.Errorf("store: ensure user: %w", err)
	}
	return u, nil
}

// UserByEmail ищет пользователя по адресу без учёта регистра.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	const q = `SELECT id::text, email, status, created_at FROM users WHERE lower(email) = lower($1)`

	var u User
	err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by email: %w", err)
	}
	return u, nil
}

// CreateKey сохраняет новый ключ (только его хеш).
func (s *Store) CreateKey(ctx context.Context, userID, keyID, name string, hash []byte, scopes []string) (APIKey, error) {
	const q = `
		INSERT INTO api_keys (user_id, key_id, key_hash, name, scopes)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text, user_id::text, key_id, name, scopes, created_at, last_used_at, revoked_at, expires_at`

	var k APIKey
	err := s.pool.QueryRow(ctx, q, userID, keyID, hash, name, scopes).Scan(
		&k.ID, &k.UserID, &k.KeyID, &k.Name, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt, &k.ExpiresAt,
	)
	if err != nil {
		return APIKey{}, fmt.Errorf("store: create key: %w", err)
	}
	return k, nil
}

// KeyByKeyID загружает ключ и статус его владельца для проверки запроса.
func (s *Store) KeyByKeyID(ctx context.Context, keyID string) (APIKey, error) {
	const q = `
		SELECT k.id::text, k.user_id::text, k.key_id, k.name, k.scopes, k.created_at,
		       k.last_used_at, k.revoked_at, k.expires_at, k.key_hash, u.status
		FROM api_keys k
		JOIN users u ON u.id = k.user_id
		WHERE k.key_id = $1`

	var k APIKey
	err := s.pool.QueryRow(ctx, q, keyID).Scan(
		&k.ID, &k.UserID, &k.KeyID, &k.Name, &k.Scopes, &k.CreatedAt,
		&k.LastUsedAt, &k.RevokedAt, &k.ExpiresAt, &k.KeyHash, &k.UserStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("store: key by key_id: %w", err)
	}
	return k, nil
}

// TouchKey обновляет дату последнего использования.
func (s *Store) TouchKey(ctx context.Context, id string) error {
	const q = `UPDATE api_keys SET last_used_at = now() WHERE id = $1::uuid`
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("store: touch key: %w", err)
	}
	return nil
}

// RevokeKey отзывает ключ. Повторный отзыв считается ошибкой «не найден».
func (s *Store) RevokeKey(ctx context.Context, id string) error {
	const q = `UPDATE api_keys SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: revoke key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeysByUser перечисляет метаданные ключей пользователя.
func (s *Store) KeysByUser(ctx context.Context, userID string) ([]APIKey, error) {
	const q = `
		SELECT id::text, user_id::text, key_id, name, scopes, created_at, last_used_at, revoked_at, expires_at
		FROM api_keys
		WHERE user_id = $1::uuid
		ORDER BY created_at DESC`

	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store: keys by user: %w", err)
	}
	defer rows.Close()

	keys := make([]APIKey, 0, 4)
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.KeyID, &k.Name, &k.Scopes,
			&k.CreatedAt, &k.LastUsedAt, &k.RevokedAt, &k.ExpiresAt); err != nil {
			return nil, fmt.Errorf("store: scan key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// Ping проверяет доступность БД для /health.
func (s *Store) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(pingCtx)
}
