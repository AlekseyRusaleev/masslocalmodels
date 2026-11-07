package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Аренда задания.
const (
	// JobLease — сколько задание закреплено за узлом без подтверждений.
	// Узел продлевает аренду каждым чанком, поэтому срок должен быть заметно
	// больше паузы между чанками на медленной модели (около 30 мс).
	JobLease = 90 * time.Second

	// JobMaxAttempts — сколько раз задание выдаётся, прежде чем закрыться
	// ошибкой. Три попытки покрывают «узел перезагрузился» и не превращают
	// одну сломанную машину в бесконечный цикл очереди.
	JobMaxAttempts = 3
)

// Usage — учтённые токены задания: вклад узла и потребление заказчика.
type Usage struct {
	JobID            string    `json:"job_id"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	EvalMS           int64     `json:"eval_ms"`
	RecordedAt       time.Time `json:"recorded_at"`
}

// UsageSummary — сводка учёта по пользователю или всей сети.
type UsageSummary struct {
	Jobs             int   `json:"jobs"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	EvalMS           int64 `json:"eval_ms"`
}

// ClaimOptions — условия выдачи задания узлу.
type ClaimOptions struct {
	// WorkerID — кто забирает.
	WorkerID string
	// Lease — на сколько закрепляем задание.
	Lease time.Duration
}

// ClaimJob ищет задание узла: приоритет, затем честная очередь.
//
// «Честная» означает, что один заказчик не занимает все узлы: среди заданий
// равного приоритета вперёд идёт то, у чьего автора меньше заданий уже в работе.
// При одном заказчике порядок остаётся обычным FIFO, поэтому правило ничего не
// ломает, но при двух и более — не даёт одному потоку запросов вытеснить чужие.
func (s *Store) ClaimJob(ctx context.Context, opts ClaimOptions) (*Job, error) {
	if opts.Lease <= 0 {
		opts.Lease = JobLease
	}

	const q = `
		WITH ready AS (
		    SELECT id, owner_user_id, served_model, served_embedding_model, trust_class
		    FROM worker_nodes
		    WHERE id = $1::uuid AND state = 'available'
		), candidate AS (
		    SELECT j.id FROM jobs j, ready r
		    WHERE j.state = 'queued'
		      AND (
		            (j.kind = 'embedding' AND r.served_embedding_model <> ''
		             AND j.model = r.served_embedding_model)
		         OR (j.kind <> 'embedding' AND r.served_model <> ''
		             AND j.model = r.served_model)
		      )
		      AND (
		            j.visibility = 'shared'
		         OR j.requester_user_id IS NOT DISTINCT FROM r.owner_user_id
		      )
		      AND (
		            r.trust_class IS DISTINCT FROM 'public'
		         OR EXISTS (
		            SELECT 1
		            FROM worker_model_digests d
		            JOIN model_catalog mc ON lower(mc.name) = lower(j.model)
		            JOIN model_revisions rev ON rev.model_id = mc.id
		              AND rev.manifest_signature <> ''
		            JOIN model_artifacts art ON art.revision_id = rev.id
		              AND art.sha256 <> ''
		              AND lower(art.sha256) = lower(replace(d.digest, 'sha256:', ''))
		            WHERE d.worker_id = r.id
		              AND lower(d.model) = lower(j.model)
		              AND d.digest <> ''
		              AND (
		                SELECT count(*) FROM model_artifacts counted
		                WHERE counted.revision_id = rev.id AND counted.sha256 <> ''
		              ) = 1
		         )
		      )
		    ORDER BY j.priority DESC,
		             (j.worker_id IS NOT NULL AND j.worker_id = $1::uuid) DESC,
		             (SELECT count(*) FROM jobs busy
		               WHERE busy.state IN ('claimed', 'streaming')
		                 AND busy.requester_user_id IS NOT DISTINCT FROM j.requester_user_id
		             ) ASC,
		             j.created_at
		    LIMIT 1
		    FOR UPDATE OF j SKIP LOCKED
		)
		UPDATE jobs j
		   SET state = 'claimed',
		       worker_id = $1::uuid,
		       claimed_at = now(),
		       attempts = j.attempts + 1,
		       lease_expires_at = now() + $2::interval
		  FROM candidate c
		 WHERE j.id = c.id
		RETURNING j.id::text, j.worker_id::text, j.model, j.kind, j.payload, j.state,
		          j.created_at, j.claimed_at, j.priority, j.attempts, j.lease_expires_at`

	var job Job
	err := s.pool.QueryRow(ctx, q, opts.WorkerID, opts.Lease.String()).Scan(
		&job.ID, &job.WorkerID, &job.Model, &job.Kind, &job.Payload, &job.State,
		&job.CreatedAt, &job.ClaimedAt, &job.Priority, &job.Attempts, &job.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim job: %w", err)
	}
	return &job, nil
}

// ExtendJobLease продлевает аренду: узел прислал чанк, значит работает.
// Возвращает false, если задание уже не принадлежит этому захвату — тогда
// узел должен прекратить работу.
func (s *Store) ExtendJobLease(ctx context.Context, jobID, workerID string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = JobLease
	}
	const q = `
		UPDATE jobs
		   SET lease_expires_at = now() + $3::interval
		 WHERE id = $1::uuid AND worker_id = $2::uuid
		   AND state IN ('claimed', 'streaming')`
	tag, err := s.pool.Exec(ctx, q, jobID, workerID, lease.String())
	if err != nil {
		return false, fmt.Errorf("store: extend lease: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReapedJob — что случилось с просроченным заданием.
type ReapedJob struct {
	ID       string
	State    string
	Attempts int
	Error    string
}

// ReapExpiredJobs возвращает в очередь задания, чей узел замолчал.
//
// Правило: повторяем только то, что ещё не начало отдавать ответ. Если узел
// умер до первого токена, клиент ничего не видел — задание можно выдать другой
// машине. Если токены уже пошли, второй исполнитель прислал бы ответ заново,
// и клиент получил бы склейку двух ответов; такое задание закрывается ошибкой.
func (s *Store) ReapExpiredJobs(ctx context.Context, maxAttempts int) ([]ReapedJob, error) {
	if maxAttempts <= 0 {
		maxAttempts = JobMaxAttempts
	}

	const q = `
		WITH expired AS (
		    SELECT id, state, attempts FROM jobs
		    WHERE state IN ('claimed', 'streaming')
		      AND lease_expires_at IS NOT NULL
		      AND lease_expires_at < now()
		    FOR UPDATE
		)
		UPDATE jobs j
		   SET state = CASE
		                 WHEN e.state = 'claimed' AND e.attempts < $1 THEN 'queued'
		                 ELSE 'failed'
		               END,
		       error = CASE
		                 WHEN e.state = 'claimed' AND e.attempts < $1
		                   THEN 'worker went silent before the first token, returned to queue'
		                 WHEN e.state = 'streaming'
		                   THEN 'worker stopped mid-answer: lease expired'
		                 ELSE 'worker stopped responding: lease expired'
		               END,
		       worker_id = CASE WHEN e.state = 'claimed' AND e.attempts < $1 THEN NULL ELSE j.worker_id END,
		       claimed_at = CASE WHEN e.state = 'claimed' AND e.attempts < $1 THEN NULL ELSE j.claimed_at END,
		       finished_at = CASE WHEN e.state = 'claimed' AND e.attempts < $1 THEN NULL ELSE now() END,
		       lease_expires_at = NULL
		  FROM expired e
		 WHERE j.id = e.id
		RETURNING j.id::text, j.state, j.attempts, j.error`

	rows, err := s.pool.Query(ctx, q, maxAttempts)
	if err != nil {
		return nil, fmt.Errorf("store: reap jobs: %w", err)
	}
	defer rows.Close()

	out := make([]ReapedJob, 0, 4)
	for rows.Next() {
		var r ReapedJob
		if err := rows.Scan(&r.ID, &r.State, &r.Attempts, &r.Error); err != nil {
			return nil, fmt.Errorf("store: scan reaped job: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordUsage записывает учёт задания ровно один раз.
//
// Возвращает false, если запись уже была: повторная доставка результата не
// должна начислять второй раз, поэтому конфликт по job_id — не ошибка.
func (s *Store) RecordUsage(ctx context.Context, job Job, userID string) (bool, error) {
	const q = `
		INSERT INTO usage_ledger (job_id, user_id, worker_id, model, prompt_tokens, completion_tokens, eval_ms)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7)
		ON CONFLICT (job_id) DO NOTHING`

	workerID := ""
	if job.WorkerID != nil {
		workerID = *job.WorkerID
	}

	tag, err := s.pool.Exec(ctx, q, job.ID, userID, workerID, job.Model,
		job.PromptTokens, job.CompletionTokens, job.EvalMS)
	if err != nil {
		return false, fmt.Errorf("store: record usage: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UsageByUser — потребление аккаунта.
func (s *Store) UsageByUser(ctx context.Context, userID string) (UsageSummary, error) {
	const q = `
		SELECT count(*), COALESCE(sum(prompt_tokens), 0), COALESCE(sum(completion_tokens), 0),
		       COALESCE(sum(eval_ms), 0)
		FROM usage_ledger WHERE user_id = $1::uuid`

	var summary UsageSummary
	if err := s.pool.QueryRow(ctx, q, userID).Scan(&summary.Jobs, &summary.PromptTokens,
		&summary.CompletionTokens, &summary.EvalMS); err != nil {
		return UsageSummary{}, fmt.Errorf("store: usage by user: %w", err)
	}
	return summary, nil
}

// UsageByWorker — вклад узла.
func (s *Store) UsageByWorker(ctx context.Context, workerID string) (UsageSummary, error) {
	const q = `
		SELECT count(*), COALESCE(sum(prompt_tokens), 0), COALESCE(sum(completion_tokens), 0),
		       COALESCE(sum(eval_ms), 0)
		FROM usage_ledger WHERE worker_id = $1::uuid`

	var summary UsageSummary
	if err := s.pool.QueryRow(ctx, q, workerID).Scan(&summary.Jobs, &summary.PromptTokens,
		&summary.CompletionTokens, &summary.EvalMS); err != nil {
		return UsageSummary{}, fmt.Errorf("store: usage by worker: %w", err)
	}
	return summary, nil
}

// SetJobPriority меняет приоритет ожидающего задания.
func (s *Store) SetJobPriority(ctx context.Context, jobID string, priority int) error {
	const q = `UPDATE jobs SET priority = $2 WHERE id = $1::uuid AND state = 'queued'`
	tag, err := s.pool.Exec(ctx, q, jobID, priority)
	if err != nil {
		return fmt.Errorf("store: set job priority: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
