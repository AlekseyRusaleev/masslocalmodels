package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Account — аккаунт с паролем и ролью. PasswordHash в JSON не попадает.
type Account struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name,omitempty"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
	HasPassword  bool      `json:"has_password"`
}

// Session — вход в панель: срок жизни и история использования.
type Session struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	KeyID      string     `json:"key_id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	UserAgent  string     `json:"user_agent,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`

	// KeyHash и статус владельца нужны проверке запроса, в JSON не идут.
	KeyHash    []byte `json:"-"`
	UserStatus string `json:"-"`
	UserRole   string `json:"-"`
}

const accountColumns = `u.id::text, u.email, u.name, u.role, u.status, u.created_at, u.password_hash`

// accountColumnsUnqualified — те же колонки без псевдонима таблицы: в
// INSERT ... RETURNING псевдонима ещё нет, и `u.` там недопустим.
const accountColumnsUnqualified = `id::text, email, name, role, status, created_at, password_hash`

// AccountByEmail загружает аккаунт для входа по паролю.
func (s *Store) AccountByEmail(ctx context.Context, email string) (Account, error) {
	const q = `SELECT ` + accountColumns + ` FROM users u WHERE lower(u.email) = lower($1)`

	var a Account
	err := s.pool.QueryRow(ctx, q, email).Scan(
		&a.ID, &a.Email, &a.Name, &a.Role, &a.Status, &a.CreatedAt, &a.PasswordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account by email: %w", err)
	}
	a.HasPassword = a.PasswordHash != ""
	return a, nil
}

// AccountByID загружает аккаунт по идентификатору.
func (s *Store) AccountByID(ctx context.Context, id string) (Account, error) {
	const q = `SELECT ` + accountColumns + ` FROM users u WHERE u.id = $1::uuid`

	var a Account
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&a.ID, &a.Email, &a.Name, &a.Role, &a.Status, &a.CreatedAt, &a.PasswordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account by id: %w", err)
	}
	a.HasPassword = a.PasswordHash != ""
	return a, nil
}

// UpsertAccount создаёт аккаунт или обновляет имя, роль и (если передан) пароль.
//
// Пустой passwordHash означает «пароль не трогать»: так администратор может
// завести человека и выдать пароль отдельным действием.
func (s *Store) UpsertAccount(ctx context.Context, email, name, role, passwordHash string) (Account, error) {
	const q = `
		INSERT INTO users (email, name, role, password_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (lower(email)) DO UPDATE SET
			name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE users.name END,
			role = CASE WHEN EXCLUDED.role <> '' THEN EXCLUDED.role ELSE users.role END,
			password_hash = CASE WHEN EXCLUDED.password_hash <> '' THEN EXCLUDED.password_hash ELSE users.password_hash END
		RETURNING ` + accountColumnsUnqualified

	var a Account
	err := s.pool.QueryRow(ctx, q, email, name, role, passwordHash).Scan(
		&a.ID, &a.Email, &a.Name, &a.Role, &a.Status, &a.CreatedAt, &a.PasswordHash,
	)
	if err != nil {
		return Account{}, fmt.Errorf("store: upsert account: %w", err)
	}
	a.HasPassword = a.PasswordHash != ""
	return a, nil
}

// SetPassword меняет пароль и отзывает все активные сессии аккаунта: старые
// входы не должны переживать смену пароля.
func (s *Store) SetPassword(ctx context.Context, userID, passwordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1::uuid`, userID, passwordHash); err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("store: revoke sessions: %w", err)
	}
	return tx.Commit(ctx)
}

// ListAccounts перечисляет аккаунты для панели.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	const q = `SELECT ` + accountColumns + ` FROM users u WHERE u.status <> 'deleted' ORDER BY u.created_at`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]Account, 0, 8)
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Status, &a.CreatedAt, &a.PasswordHash); err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		a.HasPassword = a.PasswordHash != ""
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// DisableAccount закрывает доступ: статус «удалён», сессии отозваны, ключи
// отключены.
//
// Строка остаётся в базе: на неё ссылаются задания и журнал учёта, и удалять
// историю вместе с человеком нельзя. «Удалён» означает именно отсутствие
// доступа, а не исчезновение записей.
func (s *Store) DisableAccount(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE users SET status = 'deleted' WHERE id = $1::uuid AND status <> 'deleted'`, userID)
	if err != nil {
		return fmt.Errorf("store: disable account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("store: revoke sessions: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE api_keys SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("store: revoke keys: %w", err)
	}
	return tx.Commit(ctx)
}

// CreateSession сохраняет сессию (только хеш секрета).
func (s *Store) CreateSession(ctx context.Context, userID, keyID string, hash []byte, name string,
	scopes []string, userAgent string, expiresAt time.Time) (Session, error) {
	const q = `
		INSERT INTO auth_sessions (user_id, key_id, key_hash, name, scopes, user_agent, expires_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, user_id::text, key_id, name, scopes, user_agent,
		          created_at, last_used_at, expires_at, revoked_at`

	var st Session
	err := s.pool.QueryRow(ctx, q, userID, keyID, hash, name, scopes, userAgent, expiresAt).Scan(
		&st.ID, &st.UserID, &st.KeyID, &st.Name, &st.Scopes, &st.UserAgent,
		&st.CreatedAt, &st.LastUsedAt, &st.ExpiresAt, &st.RevokedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("store: create session: %w", err)
	}
	return st, nil
}

// SessionByKeyID загружает сессию и состояние её владельца для проверки запроса.
func (s *Store) SessionByKeyID(ctx context.Context, keyID string) (Session, error) {
	const q = `
		SELECT s.id::text, s.user_id::text, s.key_id, s.name, s.scopes, s.user_agent,
		       s.created_at, s.last_used_at, s.expires_at, s.revoked_at, s.key_hash,
		       u.status, u.role
		FROM auth_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.key_id = $1`

	var st Session
	err := s.pool.QueryRow(ctx, q, keyID).Scan(
		&st.ID, &st.UserID, &st.KeyID, &st.Name, &st.Scopes, &st.UserAgent,
		&st.CreatedAt, &st.LastUsedAt, &st.ExpiresAt, &st.RevokedAt, &st.KeyHash,
		&st.UserStatus, &st.UserRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: session by key_id: %w", err)
	}
	return st, nil
}

// TouchSession обновляет дату последнего использования сессии.
func (s *Store) TouchSession(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET last_used_at = now() WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// RevokeSession завершает сессию. Повторный отзыв — «не найдено».
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("store: revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSessionByKeyID завершает сессию по публичному идентификатору из токена.
func (s *Store) RevokeSessionByKeyID(ctx context.Context, userID, keyID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE auth_sessions SET revoked_at = now()
		 WHERE key_id = $2 AND user_id = $1::uuid AND revoked_at IS NULL`, userID, keyID)
	if err != nil {
		return fmt.Errorf("store: revoke session by key_id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionsByUser перечисляет сессии аккаунта, активные первыми.
func (s *Store) SessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	const q = `
		SELECT id::text, user_id::text, key_id, name, scopes, user_agent,
		       created_at, last_used_at, expires_at, revoked_at
		FROM auth_sessions
		WHERE user_id = $1::uuid
		ORDER BY (revoked_at IS NULL) DESC, created_at DESC
		LIMIT 50`

	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store: sessions by user: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0, 8)
	for rows.Next() {
		var st Session
		if err := rows.Scan(&st.ID, &st.UserID, &st.KeyID, &st.Name, &st.Scopes, &st.UserAgent,
			&st.CreatedAt, &st.LastUsedAt, &st.ExpiresAt, &st.RevokedAt); err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		sessions = append(sessions, st)
	}
	return sessions, rows.Err()
}
