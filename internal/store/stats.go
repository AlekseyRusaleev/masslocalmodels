package store

import (
	"context"
	"fmt"
)

// PlatformStats — сводка для /metrics. Только агрегаты: ни имён узлов, ни
// адресов, ни ключей — метрики должны быть безопасными для наблюдения.
type PlatformStats struct {
	WorkersByState   map[string]int
	WorkersByClass   map[string]int
	WorkersServing   int
	JobsByState      map[string]int
	PromptTokens     int64
	CompletionTokens int64
	EvalMS           int64
	ActiveSessions   int
	AccountsByRole   map[string]int
	ActiveKeys       int
	ContextMin       int
	ContextMax       int
	// Conversations и Messages — история диалогов панели: сколько разговоров
	// и ходов сервер хранит сейчас.
	Conversations int
	Messages      int
	// Учёт: сколько токенов начислено в журнале (задания с подтверждённой моделью).
	UsageJobs       int
	UsagePrompt     int64
	UsageCompletion int64
}

// PlatformStats считает агрегаты одним проходом по таблицам.
func (s *Store) PlatformStats(ctx context.Context) (PlatformStats, error) {
	stats := PlatformStats{
		WorkersByState: make(map[string]int, 6),
		WorkersByClass: make(map[string]int, 3),
		JobsByState:    make(map[string]int, 6),
		AccountsByRole: make(map[string]int, 2),
	}

	const workerQuery = `
		SELECT state, trust_class, count(*), count(*) FILTER (WHERE served_model <> ''),
		       COALESCE(min(num_ctx) FILTER (WHERE num_ctx > 0), 0),
		       COALESCE(max(num_ctx) FILTER (WHERE num_ctx > 0), 0)
		FROM worker_nodes
		GROUP BY state, trust_class`
	rows, err := s.pool.Query(ctx, workerQuery)
	if err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats workers: %w", err)
	}
	for rows.Next() {
		var state, class string
		var total, serving, minCtx, maxCtx int
		if err := rows.Scan(&state, &class, &total, &serving, &minCtx, &maxCtx); err != nil {
			rows.Close()
			return PlatformStats{}, fmt.Errorf("store: scan stats workers: %w", err)
		}
		stats.WorkersByState[state] += total
		stats.WorkersByClass[class] += total
		stats.WorkersServing += serving
		if minCtx > 0 && (stats.ContextMin == 0 || minCtx < stats.ContextMin) {
			stats.ContextMin = minCtx
		}
		if maxCtx > stats.ContextMax {
			stats.ContextMax = maxCtx
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlatformStats{}, fmt.Errorf("store: rows stats workers: %w", err)
	}

	const jobQuery = `
		SELECT state, count(*),
		       COALESCE(sum(prompt_tokens), 0), COALESCE(sum(completion_tokens), 0),
		       COALESCE(sum(eval_ms), 0)
		FROM jobs
		GROUP BY state`
	jobRows, err := s.pool.Query(ctx, jobQuery)
	if err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats jobs: %w", err)
	}
	for jobRows.Next() {
		var state string
		var total int
		var prompt, completion, evalMS int64
		if err := jobRows.Scan(&state, &total, &prompt, &completion, &evalMS); err != nil {
			jobRows.Close()
			return PlatformStats{}, fmt.Errorf("store: scan stats jobs: %w", err)
		}
		stats.JobsByState[state] += total
		stats.PromptTokens += prompt
		stats.CompletionTokens += completion
		stats.EvalMS += evalMS
	}
	jobRows.Close()
	if err := jobRows.Err(); err != nil {
		return PlatformStats{}, fmt.Errorf("store: rows stats jobs: %w", err)
	}

	const sessionQuery = `
		SELECT count(*) FROM auth_sessions
		WHERE revoked_at IS NULL AND expires_at > now()`
	if err := s.pool.QueryRow(ctx, sessionQuery).Scan(&stats.ActiveSessions); err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats sessions: %w", err)
	}

	const accountQuery = `
		SELECT role, count(*) FROM users WHERE status = 'active' GROUP BY role`
	accountRows, err := s.pool.Query(ctx, accountQuery)
	if err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats accounts: %w", err)
	}
	for accountRows.Next() {
		var role string
		var total int
		if err := accountRows.Scan(&role, &total); err != nil {
			accountRows.Close()
			return PlatformStats{}, fmt.Errorf("store: scan stats accounts: %w", err)
		}
		stats.AccountsByRole[role] += total
	}
	accountRows.Close()
	if err := accountRows.Err(); err != nil {
		return PlatformStats{}, fmt.Errorf("store: rows stats accounts: %w", err)
	}

	const keyQuery = `SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`
	if err := s.pool.QueryRow(ctx, keyQuery).Scan(&stats.ActiveKeys); err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats keys: %w", err)
	}

	const conversationQuery = `
		SELECT (SELECT count(*) FROM conversations),
		       (SELECT count(*) FROM conversation_messages)`
	if err := s.pool.QueryRow(ctx, conversationQuery).Scan(&stats.Conversations, &stats.Messages); err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats conversations: %w", err)
	}

	const usageQuery = `
		SELECT count(*), COALESCE(sum(prompt_tokens), 0), COALESCE(sum(completion_tokens), 0)
		FROM usage_ledger`
	if err := s.pool.QueryRow(ctx, usageQuery).Scan(&stats.UsageJobs, &stats.UsagePrompt,
		&stats.UsageCompletion); err != nil {
		return PlatformStats{}, fmt.Errorf("store: stats usage: %w", err)
	}

	return stats, nil
}
