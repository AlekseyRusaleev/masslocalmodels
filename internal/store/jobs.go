package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job — задание инференса.
type Job struct {
	ID         string          `json:"id"`
	WorkerID   *string         `json:"worker_id,omitempty"`
	Model      string          `json:"model"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	State      string          `json:"state"`
	CreatedAt  time.Time       `json:"created_at"`
	ClaimedAt  *time.Time      `json:"claimed_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Error      string          `json:"error,omitempty"`

	// Очередь и аренда: приоритет, номер попытки и срок закрепления за узлом.
	Priority       int        `json:"priority"`
	Attempts       int        `json:"attempts"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`

	// Кто запросил: по нему считается потребление.
	RequesterUserID *string `json:"requester_user_id,omitempty"`

	// Чем узел реально обслужил задание: имя модели и digest весов из рантайма.
	ServedModel  string `json:"served_model,omitempty"`
	ServedDigest string `json:"served_digest,omitempty"`
	// DigestVerified — digest совпал с подписанным манифестом каталога.
	DigestVerified bool `json:"digest_verified"`
	// Attestation — итог сверки: verified, no_manifest, unsigned_manifest, mismatch, not_reported.
	Attestation string `json:"attestation,omitempty"`
	// Result — данные ответа для нестриминговых заданий (например, эмбеддинги).
	Result json.RawMessage `json:"result,omitempty"`

	// Счётчики приходят от узла: их считает рантайм, а не Control Plane.
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	EvalMS           int64  `json:"eval_ms"`
	FinishReason     string `json:"finish_reason,omitempty"`
}

// JobResult — итог работы узла над заданием.
type JobResult struct {
	Error            string `json:"error"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	EvalMS           int64  `json:"eval_ms"`
	FinishReason     string `json:"finish_reason"`

	// Чем узел обслужил задание. Пустые значения означают «узел не сообщил» —
	// так работают агенты старых версий, и это не повод закрывать задание
	// ошибкой, но повод не считать подлинность подтверждённой.
	ServedModel  string `json:"served_model,omitempty"`
	ServedDigest string `json:"served_digest,omitempty"`
	// ServedFiles — sha256 по файлам ревизии: нужен для многофайловых моделей,
	// где один digest модели не доказывает набор шардов.
	ServedFiles map[string]string `json:"served_files,omitempty"`
	// Result — данные ответа для нестриминговых заданий: узел кладёт их целиком.
	Result json.RawMessage `json:"result,omitempty"`
}

// CreateJob ставит задание в очередь.
//
// priority больше нуля поднимает задание вперёд; requesterUserID нужен, чтобы
// отнести потребление на конкретный аккаунт.
func (s *Store) CreateJob(ctx context.Context, model, kind string, payload any, priority int, requesterUserID, visibility string) (Job, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Job{}, fmt.Errorf("store: encode job: %w", err)
	}
	if visibility != "shared" {
		visibility = "private"
	}

	const q = `
		INSERT INTO jobs (model, kind, payload, priority, requester_user_id, visibility)
		VALUES ($1, $2, $3::jsonb, $4, NULLIF($5, '')::uuid, $6)
		RETURNING id::text, worker_id::text, model, kind, state, created_at, priority, attempts`

	var job Job
	if err := s.pool.QueryRow(ctx, q, model, kind, raw, priority, requesterUserID, visibility).Scan(
		&job.ID, &job.WorkerID, &job.Model, &job.Kind, &job.State, &job.CreatedAt,
		&job.Priority, &job.Attempts); err != nil {
		return Job{}, fmt.Errorf("store: create job: %w", err)
	}
	job.Payload = raw
	return job, nil
}

// ClaimJob перенесён в jobs_schedule.go: выдача задания теперь учитывает
// приоритет и аренду, поэтому живёт рядом с остальной логикой очереди.

// SetJobState переводит задание в новое состояние.
func (s *Store) SetJobState(ctx context.Context, jobID, state, errText string) error {
	const q = `
		UPDATE jobs
		   SET state = $2,
		       error = $3,
		       finished_at = CASE WHEN $2 IN ('done', 'failed', 'cancelled') THEN now() ELSE finished_at END
		 WHERE id = $1::uuid`
	tag, err := s.pool.Exec(ctx, q, jobID, state, errText)
	if err != nil {
		return fmt.Errorf("store: set job state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetJobResult закрывает задание и сохраняет счётчики, которые прислал узел.
//
// Заодно сохраняется доказательство модели: имя и digest весов из рантайма.
// По ним видно, чем узел обслужил задание, — это ответ на вопрос «а точно ли
// та модель?», который иначе проверить нечем.
func (s *Store) SetJobResult(ctx context.Context, jobID, state string, res JobResult) error {
	const q = `
		UPDATE jobs
		   SET state = $2,
		       error = $3,
		       prompt_tokens = $4,
		       completion_tokens = $5,
		       eval_ms = $6,
		       finish_reason = $7,
		       served_model = $8,
		       served_digest = $9,
		       result = $10,
		       payload = '{}'::jsonb,
		       lease_expires_at = NULL,
		       finished_at = now()
		 WHERE id = $1::uuid`
	tag, err := s.pool.Exec(ctx, q, jobID, state, res.Error, res.PromptTokens,
		res.CompletionTokens, res.EvalMS, res.FinishReason, res.ServedModel, res.ServedDigest, res.Result)
	if err != nil {
		return fmt.Errorf("store: set job result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// JobByID загружает задание.
func (s *Store) JobByID(ctx context.Context, jobID string) (Job, error) {
	const q = `
		SELECT id::text, worker_id::text, model, kind, payload, state, created_at,
		       claimed_at, finished_at, error, prompt_tokens, completion_tokens,
		       eval_ms, finish_reason, priority, attempts, lease_expires_at,
		       requester_user_id::text, served_model, served_digest, digest_verified, attestation, result
		FROM jobs WHERE id = $1::uuid`

	var job Job
	err := s.pool.QueryRow(ctx, q, jobID).Scan(&job.ID, &job.WorkerID, &job.Model, &job.Kind,
		&job.Payload, &job.State, &job.CreatedAt, &job.ClaimedAt, &job.FinishedAt, &job.Error,
		&job.PromptTokens, &job.CompletionTokens, &job.EvalMS, &job.FinishReason,
		&job.Priority, &job.Attempts, &job.LeaseExpiresAt, &job.RequesterUserID,
		&job.ServedModel, &job.ServedDigest, &job.DigestVerified, &job.Attestation, &job.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: job by id: %w", err)
	}
	return job, nil
}

// RecentJobs выдаёт последние задания для админской панели.
func (s *Store) RecentJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id::text, worker_id::text, model, kind, state, created_at, claimed_at,
		       finished_at, error, prompt_tokens, completion_tokens, eval_ms, finish_reason,
		       priority, attempts, served_model, served_digest, digest_verified, attestation
		FROM jobs ORDER BY created_at DESC LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent jobs: %w", err)
	}
	defer rows.Close()

	out := make([]Job, 0, limit)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.WorkerID, &job.Model, &job.Kind, &job.State,
			&job.CreatedAt, &job.ClaimedAt, &job.FinishedAt, &job.Error,
			&job.PromptTokens, &job.CompletionTokens, &job.EvalMS, &job.FinishReason,
			&job.Priority, &job.Attempts, &job.ServedModel, &job.ServedDigest, &job.DigestVerified, &job.Attestation); err != nil {
			return nil, fmt.Errorf("store: scan job: %w", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}
