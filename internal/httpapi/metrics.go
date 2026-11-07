package httpapi

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"
)

// registerMetricsRoutes подключает /metrics в формате Prometheus.
//
// Метрики отдаются только администратору: там видно, сколько узлов и заданий
// есть в сети, то есть косвенно — активность участников. Публично светить это
// незачем.
func (cfg Config) registerMetricsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /metrics", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		stats, err := cfg.Store.PlatformStats(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "metrics unavailable")
			return
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		var b strings.Builder
		header := func(name, kind, help string) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
		}

		header("compute_control_info", "gauge", "Версия Control Plane, всегда 1.")
		fmt.Fprintf(&b, "compute_control_info{version=%q} 1\n", cfg.Version)

		header("compute_control_uptime_seconds", "gauge", "Время работы процесса Control Plane.")
		fmt.Fprintf(&b, "compute_control_uptime_seconds %.0f\n", time.Since(cfg.StartedAt).Seconds())

		header("compute_control_goroutines", "gauge", "Живые горутины процесса.")
		fmt.Fprintf(&b, "compute_control_goroutines %d\n", runtime.NumGoroutine())

		header("compute_workers", "gauge", "Узлы сети по состоянию из worker_nodes.")
		states := make([]string, 0, len(stats.WorkersByState))
		for state := range stats.WorkersByState {
			states = append(states, state)
		}
		sort.Strings(states)
		for _, state := range states {
			fmt.Fprintf(&b, "compute_workers{state=%q} %d\n", state, stats.WorkersByState[state])
		}

		header("compute_workers_serving", "gauge", "Узлы, заявившие модель для чата.")
		fmt.Fprintf(&b, "compute_workers_serving %d\n", stats.WorkersServing)

		header("compute_jobs", "gauge", "Задания по состоянию.")
		jobStates := make([]string, 0, len(stats.JobsByState))
		for state := range stats.JobsByState {
			jobStates = append(jobStates, state)
		}
		sort.Strings(jobStates)
		for _, state := range jobStates {
			fmt.Fprintf(&b, "compute_jobs{state=%q} %d\n", state, stats.JobsByState[state])
		}

		header("compute_job_tokens_total", "counter", "Токены, обработанные узлами: вход и выход.")
		fmt.Fprintf(&b, "compute_job_tokens_total{direction=%q} %d\n", "prompt", stats.PromptTokens)
		fmt.Fprintf(&b, "compute_job_tokens_total{direction=%q} %d\n", "completion", stats.CompletionTokens)

		header("compute_job_generation_seconds_total", "counter", "Суммарное время генерации заданий.")
		fmt.Fprintf(&b, "compute_job_generation_seconds_total %.3f\n", float64(stats.EvalMS)/1000)

		header("compute_sessions_active", "gauge", "Действующие сессии панели.")
		fmt.Fprintf(&b, "compute_sessions_active %d\n", stats.ActiveSessions)

		header("compute_accounts", "gauge", "Аккаунты платформы по роли.")
		roles := make([]string, 0, len(stats.AccountsByRole))
		for role := range stats.AccountsByRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			fmt.Fprintf(&b, "compute_accounts{role=%q} %d\n", role, stats.AccountsByRole[role])
		}

		header("compute_api_keys_active", "gauge", "Действующие API-ключи (программы участников).")
		fmt.Fprintf(&b, "compute_api_keys_active %d\n", stats.ActiveKeys)

		classes := make([]string, 0, len(stats.WorkersByClass))
		for class := range stats.WorkersByClass {
			classes = append(classes, class)
		}
		sort.Strings(classes)
		header("compute_workers_by_class", "gauge", "Узлы по классу доверия.")
		for _, class := range classes {
			fmt.Fprintf(&b, "compute_workers_by_class{class=%q} %d\n", class, stats.WorkersByClass[class])
		}

		header("compute_worker_context_tokens", "gauge", "Окно контекста, заявленное узлами, в токенах.")
		fmt.Fprintf(&b, "compute_worker_context_tokens_min %d\n", stats.ContextMin)
		fmt.Fprintf(&b, "compute_worker_context_tokens_max %d\n", stats.ContextMax)

		header("compute_conversations", "gauge", "Диалоги панели, которые хранит сервер.")
		fmt.Fprintf(&b, "compute_conversations %d\n", stats.Conversations)

		header("compute_conversation_messages", "gauge", "Сообщения в этих диалогах.")
		fmt.Fprintf(&b, "compute_conversation_messages %d\n", stats.Messages)

		header("compute_usage_jobs_total", "counter", "Задания, учтённые в журнале начислений.")
		fmt.Fprintf(&b, "compute_usage_jobs_total %d\n", stats.UsageJobs)

		header("compute_usage_tokens_total", "counter", "Токены в журнале начислений: вход и выход.")
		fmt.Fprintf(&b, "compute_usage_tokens_total{direction=%q} %d\n", "prompt", stats.UsagePrompt)
		fmt.Fprintf(&b, "compute_usage_tokens_total{direction=%q} %d\n", "completion", stats.UsageCompletion)

		_, _ = w.Write([]byte(b.String()))
	}))
}
