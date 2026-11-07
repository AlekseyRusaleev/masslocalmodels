package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Conversation — диалог панели. Принадлежит одному аккаунту: чужие разговоры
// не отдаются ни списком, ни по идентификатору.
type Conversation struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Title        string    `json:"title"`
	TitleNonce   string    `json:"title_nonce,omitempty"`
	TitleKeyID   string    `json:"title_key_id,omitempty"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// ConversationMessage — одно сообщение диалога.
type ConversationMessage struct {
	ID               string    `json:"id"`
	ConversationID   string    `json:"conversation_id"`
	Ordinal          int       `json:"ordinal"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Reasoning        string    `json:"reasoning,omitempty"`
	KeyID            string    `json:"key_id,omitempty"`
	Nonce            string    `json:"nonce,omitempty"`
	Ciphertext       string    `json:"ciphertext,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	EvalMS           int       `json:"eval_ms,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// MessageInput — то, что присылает клиент при сохранении хода.
type MessageInput struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning,omitempty"`
	KeyID            string `json:"key_id,omitempty"`
	Nonce            string `json:"nonce,omitempty"`
	Ciphertext       string `json:"ciphertext,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	EvalMS           int    `json:"eval_ms,omitempty"`
}

// CreateConversation заводит диалог. Заголовок может быть пустым: панель
// подставляет первые слова запроса сама, сервер ничего не придумывает.
func (s *Store) CreateConversation(ctx context.Context, userID, title, model, titleNonce, titleKeyID string) (Conversation, error) {
	const q = `
		INSERT INTO conversations (user_id, title, model, title_nonce, title_key_id)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text, user_id::text, title, model, title_nonce, title_key_id, created_at, updated_at`

	var c Conversation
	if err := s.pool.QueryRow(ctx, q, userID, title, model, titleNonce, titleKeyID).Scan(
		&c.ID, &c.UserID, &c.Title, &c.Model, &c.TitleNonce, &c.TitleKeyID, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return Conversation{}, fmt.Errorf("store: create conversation: %w", err)
	}
	return c, nil
}

// ListConversations перечисляет диалоги аккаунта, свежие первыми.
func (s *Store) ListConversations(ctx context.Context, userID string, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT c.id::text, c.user_id::text, c.title, c.title_nonce, c.title_key_id, c.model, c.created_at, c.updated_at,
		       (SELECT count(*) FROM conversation_messages m WHERE m.conversation_id = c.id)
		FROM conversations c
		WHERE c.user_id = $1::uuid
		ORDER BY c.updated_at DESC
		LIMIT $2`

	rows, err := s.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list conversations: %w", err)
	}
	defer rows.Close()

	out := make([]Conversation, 0, 8)
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.Title, &c.TitleNonce, &c.TitleKeyID, &c.Model, &c.CreatedAt, &c.UpdatedAt,
			&c.MessageCount); err != nil {
			return nil, fmt.Errorf("store: scan conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationByID возвращает диалог, только если он принадлежит аккаунту.
func (s *Store) ConversationByID(ctx context.Context, userID, id string) (Conversation, error) {
	const q = `
		SELECT c.id::text, c.user_id::text, c.title, c.title_nonce, c.title_key_id, c.model, c.created_at, c.updated_at,
		       (SELECT count(*) FROM conversation_messages m WHERE m.conversation_id = c.id)
		FROM conversations c
		WHERE c.id = $2::uuid AND c.user_id = $1::uuid`

	var c Conversation
	err := s.pool.QueryRow(ctx, q, userID, id).Scan(
		&c.ID, &c.UserID, &c.Title, &c.TitleNonce, &c.TitleKeyID, &c.Model, &c.CreatedAt, &c.UpdatedAt, &c.MessageCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("store: conversation by id: %w", err)
	}
	return c, nil
}

// RenameConversation меняет заголовок диалога.
func (s *Store) RenameConversation(ctx context.Context, userID, id, title, titleNonce, titleKeyID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE conversations SET title = $3, title_nonce = $4, title_key_id = $5, updated_at = now()
		 WHERE id = $2::uuid AND user_id = $1::uuid`, userID, id, title, titleNonce, titleKeyID)
	if err != nil {
		return fmt.Errorf("store: rename conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteConversation удаляет диалог вместе с сообщениями.
func (s *Store) DeleteConversation(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM conversations WHERE id = $2::uuid AND user_id = $1::uuid`, userID, id)
	if err != nil {
		return fmt.Errorf("store: delete conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MessagesByConversation отдаёт сообщения по порядку.
func (s *Store) MessagesByConversation(ctx context.Context, userID, id string) ([]ConversationMessage, error) {
	if _, err := s.ConversationByID(ctx, userID, id); err != nil {
		return nil, err
	}

	const q = `
		SELECT id::text, conversation_id::text, ordinal, role, content, reasoning,
		       key_id, nonce, ciphertext,
		       prompt_tokens, completion_tokens, eval_ms, created_at
		FROM conversation_messages
		WHERE conversation_id = $1::uuid
		ORDER BY ordinal`

	rows, err := s.pool.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("store: list messages: %w", err)
	}
	defer rows.Close()

	out := make([]ConversationMessage, 0, 16)
	for rows.Next() {
		var m ConversationMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Ordinal, &m.Role, &m.Content,
			&m.Reasoning, &m.KeyID, &m.Nonce, &m.Ciphertext, &m.PromptTokens, &m.CompletionTokens, &m.EvalMS, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendMessages дописывает сообщения в конец диалога одной транзакцией.
//
// Порядковый номер считается внутри транзакции с блокировкой строки диалога:
// два одновременных хода не должны получить один и тот же ordinal. Пустой
// список — не ошибка: панель может сохранять только ответ модели.
func (s *Store) AppendMessages(ctx context.Context, userID, id string, messages []MessageInput) ([]ConversationMessage, error) {
	if len(messages) == 0 {
		return []ConversationMessage{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT true FROM conversations WHERE id = $1::uuid AND user_id = $2::uuid FOR UPDATE`,
		id, userID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: lock conversation: %w", err)
	}

	var next int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(max(ordinal) + 1, 0) FROM conversation_messages WHERE conversation_id = $1::uuid`,
		id).Scan(&next); err != nil {
		return nil, fmt.Errorf("store: next ordinal: %w", err)
	}

	saved := make([]ConversationMessage, 0, len(messages))
	for index, message := range messages {
		ordinal := next + index
		const q = `
			INSERT INTO conversation_messages
				(conversation_id, ordinal, role, content, reasoning, key_id, nonce, ciphertext,
				 prompt_tokens, completion_tokens, eval_ms)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id::text, conversation_id::text, ordinal, role, content, reasoning,
			          key_id, nonce, ciphertext,
			          prompt_tokens, completion_tokens, eval_ms, created_at`

		content, reasoning := message.Content, message.Reasoning
		if strings.TrimSpace(message.Ciphertext) != "" {
			content, reasoning = "", ""
		}
		var m ConversationMessage
		if err := tx.QueryRow(ctx, q, id, ordinal, message.Role, content, reasoning,
			message.KeyID, message.Nonce, message.Ciphertext,
			message.PromptTokens, message.CompletionTokens, message.EvalMS).Scan(
			&m.ID, &m.ConversationID, &m.Ordinal, &m.Role, &m.Content, &m.Reasoning,
			&m.KeyID, &m.Nonce, &m.Ciphertext,
			&m.PromptTokens, &m.CompletionTokens, &m.EvalMS, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: append message: %w", err)
		}
		saved = append(saved, m)
	}

	if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE id = $1::uuid`, id); err != nil {
		return nil, fmt.Errorf("store: touch conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: commit: %w", err)
	}
	return saved, nil
}

// SealMessage заменяет открытый текст сообщения шифротекстом.
func (s *Store) SealMessage(ctx context.Context, userID, conversationID, messageID, keyID, nonce, ciphertext string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages m
		   SET content = '',
		       reasoning = '',
		       key_id = $4,
		       nonce = $5,
		       ciphertext = $6
		  FROM conversations c
		 WHERE m.id = $3::uuid
		   AND m.conversation_id = c.id
		   AND c.id = $2::uuid
		   AND c.user_id = $1::uuid`,
		userID, conversationID, messageID, keyID, nonce, ciphertext)
	if err != nil {
		return fmt.Errorf("store: seal message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
