package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/private-compute/network/internal/store"
)

// piece — один кусок ответа модели: обычный текст, «размышления» и/или вызовы
// инструментов.
type piece struct {
	Content   string
	Thinking  string
	ToolCalls []ToolCallPiece
}

// ToolCallPiece — вызов инструмента в потоке: имя и аргументы строкой JSON,
// как их ждёт клиент OpenAI.
type ToolCallPiece struct {
	Name      string
	Arguments string
}

// hub связывает ожидающий HTTP-стрим с чанками, которые присылает узел.
type hub struct {
	mu      sync.Mutex
	streams map[string]*stream
}

type stream struct {
	chunks chan piece
	done   chan struct{}
	failed chan string
	result chan store.JobResult
	once   sync.Once
}

func newHub() *hub {
	return &hub{streams: make(map[string]*stream)}
}

func (h *hub) open(jobID string) *stream {
	s := &stream{
		chunks: make(chan piece, 64),
		done:   make(chan struct{}),
		failed: make(chan string, 1),
		result: make(chan store.JobResult, 1),
	}
	h.mu.Lock()
	h.streams[jobID] = s
	h.mu.Unlock()
	return s
}

func (h *hub) get(jobID string) (*stream, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.streams[jobID]
	return s, ok
}

func (h *hub) close(jobID string) {
	h.mu.Lock()
	delete(h.streams, jobID)
	h.mu.Unlock()
}

// finish закрывает стрим ровно один раз.
func (s *stream) finish(res store.JobResult) {
	s.once.Do(func() {
		select {
		case s.result <- res:
		default:
		}
		if strings.TrimSpace(res.Error) != "" {
			select {
			case s.failed <- res.Error:
			default:
			}
		}
		close(s.done)
	})
}

// outcome забирает итог задания; после finish он всегда есть.
func (s *stream) outcome() store.JobResult {
	select {
	case res := <-s.result:
		return res
	default:
		return store.JobResult{}
	}
}

const (
	// pullTimeout — сколько узел ждёт задание в long-poll.
	pullTimeout = 25 * time.Second
	// chatTimeout — сколько клиент ждёт ответ.
	// Компьютер человека почти всегда за VPN, сервер стоит в России, и загрузка
	// большой модели по этому пути молчит дольше десяти минут. Короткий обрыв
	// закрывал запрос раньше, чем узел успевал поднять слои.
	chatTimeout = 2*time.Hour + 10*time.Minute
	// servingWindow — сколько живёт заявка узла «я обслуживаю эту модель».
	// Три heartbeat подряд: короткий сбой связи не убирает модель из каталога.
	servingWindow = 3 * time.Minute
)

// registerInferenceRoutes подключает публичный chat completions и работу узла с заданиями.
func (cfg Config) registerInferenceRoutes(mux *http.ServeMux) {
	hubRef := cfg.Hub
	if hubRef == nil {
		hubRef = newHub()
	}

	mux.Handle("POST /v1/chat/completions", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		if !cfg.rateLimited(w, p) {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot read body")
			return
		}
		request, err := ParseChatRequest(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		nodes, err := cfg.Store.ServingNodesForOwner(r.Context(), request.Model, p.UserID, servingWindow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot check model availability")
			return
		}
		if len(nodes) == 0 {
			writeError(w, http.StatusConflict, "no_owned_worker",
				"chat runs only on your machines, and none of them is serving "+request.Model+availableSuffix(r, cfg, request.Model))
			return
		}

		job, err := cfg.Store.CreateJob(r.Context(), request.Model, "chat", request, requestPriority(r), p.UserID, "private")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create job")
			return
		}
		streamRef := hubRef.open(job.ID)
		defer hubRef.close(job.ID)

		timeout := time.NewTimer(chatTimeout)
		defer timeout.Stop()

		if request.Stream {
			serveStreamingChat(w, r, job, streamRef, timeout, cfg)
			return
		}
		collectChat(w, r, job, streamRef, timeout, cfg)
	}))

	// Статус задания: без содержимого сообщений.
	mux.Handle("GET /v1/jobs/{id}", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, _ principal) {
		job, err := cfg.Store.JobByID(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": job.ID, "model": job.Model, "state": job.State,
			"created_at": job.CreatedAt, "claimed_at": job.ClaimedAt,
			"finished_at": job.FinishedAt, "error": job.Error,
			"prompt_tokens": job.PromptTokens, "completion_tokens": job.CompletionTokens,
			"eval_ms": job.EvalMS, "finish_reason": job.FinishReason,
			// Очередь и аренда: видно, сколько раз задание выдавалось и до
			// какого момента закреплено за узлом.
			"priority": job.Priority, "attempts": job.Attempts,
			"lease_expires_at": job.LeaseExpiresAt,
			// Чем узел реально обслужил задание: имя и digest весов.
			"served_model": job.ServedModel, "served_digest": job.ServedDigest,
			// Сверен ли digest с манифестом каталога.
			"digest_verified": job.DigestVerified, "attestation": job.Attestation,
		})
	}))

	// Узел забирает задание: long-poll, чтобы не крутить пустые запросы.
	mux.Handle("POST /worker/v1/jobs/pull", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		deadline := time.Now().Add(pullTimeout)
		for {
			job, err := cfg.Store.ClaimJob(r.Context(), store.ClaimOptions{WorkerID: wp.Worker.ID, Lease: store.JobLease})
			if err == nil {
				writeJSON(w, http.StatusOK, job)
				return
			}
			if r.Context().Err() != nil {
				writeError(w, http.StatusRequestTimeout, "cancelled", "client gone")
				return
			}
			if time.Now().After(deadline) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}))

	// Чанк ответа модели: текст, «размышления» и вызовы инструментов.
	mux.Handle("POST /worker/v1/jobs/{id}/chunk", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		jobID := r.PathValue("id")
		var body struct {
			Content   string `json:"content"`
			Thinking  string `json:"thinking"`
			ToolCalls []struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"tool_calls"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		streamRef, ok := hubRef.get(jobID)
		if !ok {
			writeError(w, http.StatusConflict, "no_stream", "job stream is not waiting")
			return
		}
		calls := make([]ToolCallPiece, 0, len(body.ToolCalls))
		for _, call := range body.ToolCalls {
			calls = append(calls, ToolCallPiece{Name: call.Name, Arguments: call.Arguments})
		}
		if !cfg.holdLease(w, r, jobID, wp.Worker.ID) {
			return
		}
		_ = cfg.Store.SetJobState(r.Context(), jobID, "streaming", "")
		select {
		case streamRef.chunks <- piece{Content: body.Content, Thinking: body.Thinking, ToolCalls: calls}:
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
		case <-time.After(5 * time.Second):
			writeError(w, http.StatusGatewayTimeout, "slow_consumer", "client is not reading")
		}
	}))

	// Поток чанков: узел отправляет их одним запросом с телом в формате NDJSON,
	// сервер раздаёт их клиенту по мере поступления. Это убирает ожидание
	// round-trip на каждый токен — при RTT 50–250 мс разница в разы.
	mux.Handle("POST /worker/v1/jobs/{id}/chunkstream", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		jobID := r.PathValue("id")
		streamRef, ok := hubRef.get(jobID)
		if !ok {
			writeError(w, http.StatusConflict, "no_stream", "job stream is not waiting")
			return
		}
		if !cfg.holdLease(w, r, jobID, wp.Worker.ID) {
			return
		}
		_ = cfg.Store.SetJobState(r.Context(), jobID, "streaming", "")

		decoder := json.NewDecoder(bufio.NewReaderSize(r.Body, 32<<10))
		sent := 0
		for {
			var body struct {
				Content   string `json:"content"`
				Thinking  string `json:"thinking"`
				ToolCalls []struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"tool_calls"`
			}
			err := decoder.Decode(&body)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "cannot read chunk stream: "+err.Error())
				return
			}
			calls := make([]ToolCallPiece, 0, len(body.ToolCalls))
			for _, call := range body.ToolCalls {
				calls = append(calls, ToolCallPiece{Name: call.Name, Arguments: call.Arguments})
			}
			// Продление раз в 20 чанков: одно обновление на пачку токенов, а не на
			// каждый — при 400 чанках это 20 запросов вместо 400.
			if sent%20 == 0 {
				ok, err := cfg.Store.ExtendJobLease(r.Context(), jobID, wp.Worker.ID, store.JobLease)
				if err != nil {
					cfg.Log.Warn("cannot extend job lease", "job", jobID, "err", err)
					writeError(w, http.StatusInternalServerError, "internal", "cannot extend job lease")
					return
				}
				if !ok {
					writeError(w, http.StatusConflict, "lease_mismatch", "this job is not leased to this worker")
					return
				}
			}
			select {
			case streamRef.chunks <- piece{Content: body.Content, Thinking: body.Thinking, ToolCalls: calls}:
				sent++
			case <-time.After(10 * time.Second):
				writeError(w, http.StatusGatewayTimeout, "slow_consumer", "client is not reading")
				return
			case <-r.Context().Done():
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "chunks": sent})
	}))

	// Завершение задания: причина остановки и счётчики рантайма.
	mux.Handle("POST /worker/v1/jobs/{id}/done", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		jobID := r.PathValue("id")
		var body store.JobResult
		_ = decodeJSON(w, r, &body)

		if !cfg.holdLease(w, r, jobID, wp.Worker.ID) {
			return
		}

		job, err := cfg.Store.JobByID(r.Context(), jobID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}

		// Проверка подлинности: узел обязан сказать, чем он обслужил задание.
		// Если он назвал другую модель, ответ не тот, который просили, — это
		// ошибка, а не «мелкое расхождение в имени».
		if mismatch := modelMismatch(job.Model, body.ServedModel); mismatch != "" {
			body.Error = mismatch
		}

		// Сверка весов с подписанным манифестом каталога. Манифеста может не
		// быть — тогда задание проходит, но не считается проверенным: требовать
		// каталог от узла с Ollama-моделью было бы нечестно. А вот расхождение
		// хэшей или испорченная подпись — это ошибка, и молчать о ней нельзя.
		verified := false
		verdict := store.AttestationNoManifest
		if body.Error == "" {
			attestation, err := cfg.Store.VerifyAttestation(r.Context(), cfg.Pepper, job.Model, body.ServedDigest, body.ServedFiles)
			switch {
			case err != nil:
				cfg.Log.Warn("weight attestation failed to run", "job", jobID, "err", err)
			default:
				verdict = attestation.Verdict
				switch attestation.Verdict {
				case store.AttestationVerified:
					verified = true
				case store.AttestationMismatch:
					body.Error = "weight attestation failed: " + attestation.Detail
				default:
					cfg.Log.Info("weight attestation skipped",
						"job", jobID, "verdict", attestation.Verdict, "detail", attestation.Detail)
				}
			}
		}
		if err := cfg.Store.SetJobAttestation(r.Context(), jobID, verdict); err != nil {
			cfg.Log.Warn("cannot store job attestation", "job", jobID, "err", err)
		}

		state := "done"
		if strings.TrimSpace(body.Error) != "" {
			state = "failed"
		}
		if err := cfg.Store.SetJobResult(r.Context(), jobID, state, body); err != nil {
			cfg.Log.Warn("cannot store job result", "job", jobID, "err", err)
		}
		if verified {
			if err := cfg.Store.SetJobVerified(r.Context(), jobID, true); err != nil {
				cfg.Log.Warn("cannot mark job verified", "job", jobID, "err", err)
			}
		}

		// Учёт вклада: одна запись на задание. Повторная доставка результата
		// (перезапуск узла, повтор запроса) не должна начислять второй раз —
		// поэтому конфликт по job_id не ошибка, а признак «уже учтено».
		if state == "done" {
			requester := ""
			if job.RequesterUserID != nil {
				requester = *job.RequesterUserID
			}
			job.PromptTokens = body.PromptTokens
			job.CompletionTokens = body.CompletionTokens
			job.EvalMS = body.EvalMS
			job.ServedModel = body.ServedModel
			job.ServedDigest = body.ServedDigest
			if recorded, err := cfg.Store.RecordUsage(r.Context(), job, requester); err != nil {
				cfg.Log.Warn("cannot record usage", "job", jobID, "err", err)
			} else if !recorded {
				cfg.Log.Info("usage already recorded, skipping", "job", jobID)
			}
		}

		if streamRef, ok := hubRef.get(jobID); ok {
			streamRef.finish(body)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "state": state})
	}))

	// Приоритет ожидающего задания: администратор поднимает важное вперёд.
	mux.Handle("POST /admin/v1/jobs/{id}/priority", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		jobID := r.PathValue("id")
		var body struct {
			Priority int `json:"priority"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.Priority < -100 || body.Priority > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "priority must be between -100 and 100")
			return
		}
		if err := cfg.Store.SetJobPriority(r.Context(), jobID, body.Priority); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "queued job not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot set priority")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "priority": body.Priority})
	}))

	// Эмбеддинги: тот же конвейер заданий, но без потока — результат целиком.
	cfg.registerEmbeddingRoutes(mux, hubRef)

	// База знаний: загрузка документов и поиск по векторам.
	cfg.registerRAGRoutes(mux, hubRef)

	// Список последних заданий для админской панели.
	mux.Handle("GET /admin/v1/jobs", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		jobs, err := cfg.Store.RecentJobs(r.Context(), 50)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list jobs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": jobs})
	}))
}

// requestPriority читает приоритет задания из заголовка.
//
// Приоритет — это внутренняя настройка платформы, а не часть OpenAI-протокола:
// клиент вроде харнеса о нём не знает. Заголовок выбран, чтобы не менять тело
// запроса и не ломать совместимость.
func requestPriority(r *http.Request) int {
	raw := strings.TrimSpace(r.Header.Get("X-Compute-Priority"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < -100 || value > 100 {
		return 0
	}
	return value
}

// holdLease продлевает аренду и отказывает, если задание закреплено не за этим узлом.
func (cfg Config) holdLease(w http.ResponseWriter, r *http.Request, jobID, workerID string) bool {
	ok, err := cfg.Store.ExtendJobLease(r.Context(), jobID, workerID, store.JobLease)
	if err != nil {
		cfg.Log.Warn("cannot extend job lease", "job", jobID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "cannot extend job lease")
		return false
	}
	if !ok {
		writeError(w, http.StatusConflict, "lease_mismatch", "this job is not leased to this worker")
		return false
	}
	return true
}

// modelMismatch сверяет запрошенную модель с тем, что узел реально обслужил.
//
// Пустое имя означает «узел не сообщил» (старые агенты) — это не расхождение.
// Сравнение мягкое к регистру и к суффиксу :latest, но не к имени: qwen3:14b
// вместо qwen3:8b — это другой ответ на другой модели, и молчать об этом нельзя.
func modelMismatch(requested, served string) string {
	normalize := func(name string) string {
		name = strings.ToLower(strings.TrimSpace(name))
		return strings.TrimSuffix(name, ":latest")
	}
	served = normalize(served)
	if served == "" {
		return ""
	}
	if served != normalize(requested) {
		return fmt.Sprintf("model substitution: requested %q, worker served %q", requested, served)
	}
	return ""
}

// runJobReaper возвращает в очередь задания, чей узел замолчал.
//
// Без этого просроченный захват означал бы вечное ожидание: клиент держит
// стрим, задание помечено как выполняемое, а выполнять его некому.
func (cfg Config) runJobReaper(h *hub) {
	const interval = 15 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		reaped, err := cfg.Store.ReapExpiredJobs(ctx, store.JobMaxAttempts)
		cancel()
		if err != nil {
			cfg.Log.Warn("job reaper failed", "err", err)
			continue
		}
		for _, job := range reaped {
			if job.State == "failed" {
				// Клиент ждёт ответа, которого не будет: закрываем поток
				// честной ошибкой, а не таймаутом через десять минут.
				if streamRef, ok := h.get(job.ID); ok {
					streamRef.finish(store.JobResult{Error: job.Error})
				}
			}
			cfg.Log.Info("job lease expired",
				"job", job.ID, "state", job.State, "attempts", job.Attempts)
		}
	}
}

// availableSuffix перечисляет модели, которые сейчас обслуживаются, чтобы
// сообщение об ошибке сразу подсказывало правильное имя.
func availableSuffix(r *http.Request, cfg Config, requested string) string {
	serving, err := cfg.Store.ServingModels(r.Context(), servingWindow)
	if err != nil || len(serving) == 0 {
		return "; no worker reports a served model yet"
	}
	names := make([]string, 0, len(serving))
	for _, m := range serving {
		if m.ID != requested {
			names = append(names, m.ID)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "; available: " + strings.Join(names, ", ")
}

// serveStreamingChat отдаёт ответ через SSE по мере прихода чанков.
func serveStreamingChat(w http.ResponseWriter, r *http.Request, job store.Job, s *stream, timeout *time.Timer, cfg Config) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	completionID := "chatcmpl-" + job.ID

	// Номер вызова инструмента в потоке и счётчик выданных id: OpenAI нумерует
	// вызовы по порядку, а клиенты (харнес в том числе) склеивают по index.
	toolIndex := 0
	hadToolCalls := false

	writeDelta := func(p piece, finishReason any) bool {
		delta := map[string]any{}
		if p.Content != "" {
			delta["content"] = p.Content
		}
		if p.Thinking != "" {
			delta["reasoning_content"] = p.Thinking
		}
		if len(p.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(p.ToolCalls))
			for _, call := range p.ToolCalls {
				calls = append(calls, map[string]any{
					"index": toolIndex,
					"id":    fmt.Sprintf("call_%s_%d", job.ID[:8], toolIndex),
					"type":  "function",
					"function": map[string]string{
						"name":      call.Name,
						"arguments": call.Arguments,
					},
				})
				toolIndex++
			}
			delta["tool_calls"] = calls
			hadToolCalls = true
		}
		payload := map[string]any{
			"id":      completionID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   job.Model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			}},
		}
		raw, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeDelta(piece{}, nil) {
		return
	}

	for {
		select {
		case p := <-s.chunks:
			if !writeDelta(p, nil) {
				return
			}
		case <-s.done:
			res := s.outcome()
			if res.Error != "" {
				writeSSEError(w, flusher, "inference_failed", res.Error)
			}
			finish := res.FinishReason
			if finish == "" {
				finish = "stop"
			}
			if hadToolCalls {
				finish = "tool_calls"
			}
			writeDelta(piece{}, finish)
			writeUsage(w, flusher, res)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		case <-timeout.C:
			markFailed(cfg, r, job.ID, "timeout waiting for worker")
			writeSSEError(w, flusher, "timeout", "worker did not answer in time")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

// collectChat собирает ответ целиком и отдаёт обычный JSON.
func collectChat(w http.ResponseWriter, r *http.Request, job store.Job, s *stream, timeout *time.Timer, cfg Config) {
	var content, thinking strings.Builder
	var calls []map[string]any

	for {
		select {
		case p := <-s.chunks:
			content.WriteString(p.Content)
			thinking.WriteString(p.Thinking)
			for _, call := range p.ToolCalls {
				index := len(calls)
				calls = append(calls, map[string]any{
					"index":    index,
					"id":       fmt.Sprintf("call_%s_%d", job.ID[:8], index),
					"type":     "function",
					"function": map[string]string{"name": call.Name, "arguments": call.Arguments},
				})
			}
		case <-s.done:
			res := s.outcome()
			if res.Error != "" {
				writeError(w, http.StatusBadGateway, "inference_failed", res.Error)
				return
			}
			finish := res.FinishReason
			if finish == "" {
				finish = "stop"
			}
			message := map[string]any{"role": "assistant", "content": content.String()}
			if thinking.Len() > 0 {
				message["reasoning_content"] = thinking.String()
			}
			if len(calls) > 0 {
				finish = "tool_calls"
				// Клиенту OpenAI нужны вызовы без служебного index.
				clean := make([]map[string]any, 0, len(calls))
				for _, call := range calls {
					clean = append(clean, map[string]any{
						"id":       call["id"],
						"type":     "function",
						"function": call["function"],
					})
				}
				message["tool_calls"] = clean
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id":      "chatcmpl-" + job.ID,
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   job.Model,
				"choices": []map[string]any{{
					"index":         0,
					"message":       message,
					"finish_reason": finish,
				}},
				"usage": usageOf(res),
			})
			return
		case <-timeout.C:
			markFailed(cfg, r, job.ID, "timeout waiting for worker")
			writeError(w, http.StatusGatewayTimeout, "timeout", "worker did not answer in time")
			return
		case <-r.Context().Done():
			markFailed(cfg, r, job.ID, "client cancelled")
			return
		}
	}
}

// usageOf отдаёт счётчики узла в формате OpenAI. Нули остаются нулями:
// если рантайм не сообщил цифры, мы их не выдумываем.
func usageOf(res store.JobResult) map[string]any {
	usage := map[string]any{
		"prompt_tokens":     res.PromptTokens,
		"completion_tokens": res.CompletionTokens,
		"total_tokens":      res.PromptTokens + res.CompletionTokens,
	}
	// Скорость считаем только по времени генерации, без загрузки модели.
	if res.EvalMS > 0 && res.CompletionTokens > 0 {
		usage["tokens_per_second"] = float64(res.CompletionTokens) * 1000 / float64(res.EvalMS)
		usage["eval_ms"] = res.EvalMS
	}
	return usage
}

// writeUsage отправляет финальное событие с usage: OpenAI в стриме присылает
// его отдельным чанком с пустым choices, клиенты это понимают.
func writeUsage(w http.ResponseWriter, flusher http.Flusher, res store.JobResult) {
	payload := map[string]any{
		"object":  "chat.completion.usage",
		"choices": []any{},
		"usage":   usageOf(res),
	}
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	flusher.Flush()
}

// writeSSEError формирует событие ошибки в формате стрима.
func writeSSEError(w http.ResponseWriter, flusher http.Flusher, code, message string) {
	payload := map[string]any{"error": map[string]string{"code": code, "message": message}}
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	flusher.Flush()
}

// markFailed фиксирует провал задания, не блокируя ответ клиенту.
func markFailed(cfg Config, r *http.Request, jobID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cfg.Store.SetJobState(ctx, jobID, "failed", reason)
}
