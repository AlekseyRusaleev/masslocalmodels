package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Enrollment — одноразовый токен подключения устройства.
type Enrollment struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	TokenID     string     `json:"-"`
	TokenHash   []byte     `json:"-"`
	Label       string     `json:"label"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	WorkerID    *string    `json:"worker_id,omitempty"`
}

// Worker — зарегистрированный узел.
type Worker struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
	State       string     `json:"state"`
	TrustClass  string     `json:"trust_class"`
	AgentVer    string     `json:"agent_version"`
	OS          string     `json:"os"`
	Hostname    string     `json:"hostname"`
	EnrolledAt  time.Time  `json:"enrolled_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	// ServedModel — модель, заявленная узлом в последнем heartbeat.
	ServedModel string `json:"served_model,omitempty"`
	// NumCtx — окно контекста в токенах из последнего heartbeat.
	// 0 означает «узел не сообщил».
	NumCtx int `json:"num_ctx,omitempty"`
	// ServedEmbeddingModel — модель эмбеддингов, которую узел готов считать.
	ServedEmbeddingModel string `json:"served_embedding_model,omitempty"`
	TokenID              string `json:"-"`
	TokenHash            []byte `json:"-"`
}

// GPU описывает одну видеокарту узла.
type GPU struct {
	Name       string `json:"name"`
	VRAMBytes  int64  `json:"vram_bytes"`
	DriverVer  string `json:"driver_version,omitempty"`
	CUDAVer    string `json:"cuda_version,omitempty"`
	ComputeCap string `json:"compute_capability,omitempty"`
}

// Disk описывает точку монтирования и свободное место.
// Kind: "local" (физический/локальный том), "removable" (съёмный).
// Облачные и сетевые диски в capabilities не попадают.
type Disk struct {
	Mount      string `json:"mount"`
	FreeBytes  int64  `json:"free_bytes"`
	TotalBytes int64  `json:"total_bytes"`
	Kind       string `json:"kind,omitempty"`
}

// Capabilities — результат hardware discovery на узле.
type Capabilities struct {
	CPUModel      string         `json:"cpu_model"`
	CPUCores      int            `json:"cpu_cores"`
	RAMBytes      int64          `json:"ram_bytes"`
	GPUs          []GPU          `json:"gpus"`
	Disks         []Disk         `json:"disks"`
	CUDAVersion   string         `json:"cuda_version"`
	DriverVersion string         `json:"driver_version"`
	Network       map[string]any `json:"network"`
}

// WorkerListRow — строка админской таблицы узлов.
type WorkerListRow struct {
	Worker
	CPUCores      *int       `json:"cpu_cores,omitempty"`
	RAMBytes      *int64     `json:"ram_bytes,omitempty"`
	GPUCount      int        `json:"gpu_count"`
	GPUVRAMBytes  int64      `json:"gpu_vram_bytes"`
	DiskFreeBytes *int64     `json:"disk_free_bytes,omitempty"`
	LastHBState   *string    `json:"last_heartbeat_state,omitempty"`
	LastHBAt      *time.Time `json:"last_heartbeat_at,omitempty"`
	RTTMS         *int       `json:"rtt_ms,omitempty"`
	BenchTokensPS *float32   `json:"bench_tokens_per_second,omitempty"`
	BenchBillions *float32   `json:"bench_model_billions,omitempty"`
	BenchAt       *time.Time `json:"bench_at,omitempty"`
	// MSPerBillion — нормированная скорость узла: мс на 1 млрд параметров.
	MSPerBillion *float64 `json:"ms_per_billion,omitempty"`
	// Из последнего heartbeat: панель показывает реальную загрузку, а не догадку.
	ActiveJobs    *int     `json:"active_jobs,omitempty"`
	VRAMFreeBytes *int64   `json:"vram_free_bytes,omitempty"`
	TemperatureC  *float32 `json:"temperature_c,omitempty"`
	CPUPercent    *float32 `json:"cpu_percent,omitempty"`
	// Последние задания узла: сколько токенов он отдал и как быстро.
	JobsDone         int      `json:"jobs_done"`
	JobsFailed       int      `json:"jobs_failed"`
	LastTokensPerSec *float64 `json:"last_tokens_per_second,omitempty"`
}

// SaveBenchmark сохраняет результат калибровки узла.
func (s *Store) SaveBenchmark(ctx context.Context, workerID string, billions float64, quantization string, tokensPerSecond float64) error {
	const q = `
		UPDATE worker_capabilities
		   SET bench_model_billions = $2,
		       bench_quantization = $3,
		       bench_tokens_per_second = $4,
		       bench_at = now(),
		       updated_at = now()
		 WHERE worker_id = $1::uuid`
	tag, err := s.pool.Exec(ctx, q, workerID, billions, quantization, tokensPerSecond)
	if err != nil {
		return fmt.Errorf("store: save benchmark: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Heartbeat — телеметрия узла.
type Heartbeat struct {
	WorkerID      string         `json:"worker_id"`
	State         string         `json:"state"`
	CPUPercent    *float32       `json:"cpu_percent,omitempty"`
	RAMFreeBytes  *int64         `json:"ram_free_bytes,omitempty"`
	VRAMFreeBytes *int64         `json:"vram_free_bytes,omitempty"`
	DiskFreeBytes *int64         `json:"disk_free_bytes,omitempty"`
	TemperatureC  *float32       `json:"temperature_c,omitempty"`
	PowerWatts    *float32       `json:"power_watts,omitempty"`
	ActiveJobs    *int           `json:"active_jobs,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

// CreateEnrollment сохраняет одноразовый токен подключения.
func (s *Store) CreateEnrollment(ctx context.Context, ownerUserID, tokenID, label string, hash []byte, ttl time.Duration) (Enrollment, error) {
	const q = `
		INSERT INTO worker_enrollments (owner_user_id, token_id, token_hash, label, expires_at)
		VALUES ($1::uuid, $2, $3, $4, now() + $5::interval)
		RETURNING id::text, owner_user_id::text, label, created_at, expires_at, used_at, worker_id::text`

	var e Enrollment
	var workerID *string
	err := s.pool.QueryRow(ctx, q, ownerUserID, tokenID, hash, label, fmt.Sprintf("%d seconds", int(ttl.Seconds()))).
		Scan(&e.ID, &e.OwnerUserID, &e.Label, &e.CreatedAt, &e.ExpiresAt, &e.UsedAt, &workerID)
	if err != nil {
		return Enrollment{}, fmt.Errorf("store: create enrollment: %w", err)
	}
	e.TokenID = tokenID
	e.WorkerID = workerID
	return e, nil
}

// EnrollmentByTokenID загружает токен подключения для проверки.
func (s *Store) EnrollmentByTokenID(ctx context.Context, tokenID string) (Enrollment, error) {
	const q = `
		SELECT id::text, owner_user_id::text, token_id, token_hash, label, created_at, expires_at, used_at, worker_id::text
		FROM worker_enrollments WHERE token_id = $1`

	var e Enrollment
	var workerID *string
	err := s.pool.QueryRow(ctx, q, tokenID).Scan(&e.ID, &e.OwnerUserID, &e.TokenID, &e.TokenHash,
		&e.Label, &e.CreatedAt, &e.ExpiresAt, &e.UsedAt, &workerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, ErrNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("store: enrollment by token: %w", err)
	}
	e.WorkerID = workerID
	return e, nil
}

// RegisterWorker атомарно гасит enrollment и создаёт узел с его возможностями.
func (s *Store) RegisterWorker(ctx context.Context, enrollmentID, workerName, hostname, osName, agentVersion, deviceTokenID string, deviceHash []byte, caps Capabilities) (Worker, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Worker{}, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID string
	var usedAt *time.Time
	const lockQ = `
		SELECT owner_user_id::text, used_at FROM worker_enrollments WHERE id = $1::uuid FOR UPDATE`
	if err := tx.QueryRow(ctx, lockQ, enrollmentID).Scan(&ownerID, &usedAt); err != nil {
		return Worker{}, fmt.Errorf("store: lock enrollment: %w", err)
	}
	if usedAt != nil {
		return Worker{}, ErrNotFound
	}

	var w Worker
	// Имя узла уникально: повторная установка на уже зарегистрированную машину
	// должна получить понятный ответ, а не «internal error».
	const insertWorker = `
		INSERT INTO worker_nodes (owner_user_id, name, hostname, os, agent_version, device_token_id, device_token_hash)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, owner_user_id::text, name, state, trust_class, agent_version, os, hostname, enrolled_at, last_seen_at`
	err = tx.QueryRow(ctx, insertWorker, ownerID, workerName, hostname, osName, agentVersion, deviceTokenID, deviceHash).
		Scan(&w.ID, &w.OwnerUserID, &w.Name, &w.State, &w.TrustClass, &w.AgentVer, &w.OS, &w.Hostname, &w.EnrolledAt, &w.LastSeenAt)
	if err != nil {
		// Уникальность имени узла: повторная установка на уже зарегистрированную
		// машину должна получить понятный ответ, а не «internal error».
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Worker{}, ErrNameTaken
		}
		return Worker{}, fmt.Errorf("store: insert worker: %w", err)
	}

	gpus, _ := json.Marshal(caps.GPUs)
	disks, _ := json.Marshal(caps.Disks)
	netw, _ := json.Marshal(caps.Network)
	if caps.GPUs == nil {
		gpus = []byte("[]")
	}
	if caps.Disks == nil {
		disks = []byte("[]")
	}
	if caps.Network == nil {
		netw = []byte("{}")
	}

	const insertCaps = `
		INSERT INTO worker_capabilities (worker_id, cpu_model, cpu_cores, ram_bytes, gpu_json, disk_json,
			cuda_version, driver_version, network_json)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9::jsonb)`
	if _, err := tx.Exec(ctx, insertCaps, w.ID, caps.CPUModel, caps.CPUCores, caps.RAMBytes,
		gpus, disks, caps.CUDAVersion, caps.DriverVersion, netw); err != nil {
		return Worker{}, fmt.Errorf("store: insert capabilities: %w", err)
	}

	const markUsed = `UPDATE worker_enrollments SET used_at = now(), worker_id = $2::uuid WHERE id = $1::uuid`
	if _, err := tx.Exec(ctx, markUsed, enrollmentID, w.ID); err != nil {
		return Worker{}, fmt.Errorf("store: mark enrollment used: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Worker{}, fmt.Errorf("store: commit: %w", err)
	}
	return w, nil
}

// RegisterSelfWorker создаёт узел без приглашения: самозапись.
//
// Участник не должен зависеть от владельца сети. Узел получает собственный
// аккаунт (он нужен для ключа API и учёта), класс доверия public — сеть видит,
// что машина пришла сама. Владелец может потом поднять класс или вывести узел.
func (s *Store) RegisterSelfWorker(ctx context.Context, workerName, hostname, osName, agentVersion,
	deviceTokenID string, deviceHash []byte, caps Capabilities) (Worker, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Worker{}, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Аккаунт узла: почта синтетическая, вход в панель не нужен — участнику
	// нужен только ключ API для харнеса.
	email := "node-" + strings.ToLower(deviceTokenID) + "@network.local"
	var ownerID string
	const insertUser = `
		INSERT INTO users (email, name, role) VALUES ($1, 'Узел сети', 'user')
		ON CONFLICT (lower(email)) DO UPDATE SET email = EXCLUDED.email
		RETURNING id::text`
	if err := tx.QueryRow(ctx, insertUser, email).Scan(&ownerID); err != nil {
		return Worker{}, fmt.Errorf("store: create node account: %w", err)
	}

	var w Worker
	const insertWorker = `
		INSERT INTO worker_nodes (owner_user_id, name, hostname, os, agent_version, trust_class,
			device_token_id, device_token_hash)
		VALUES ($1::uuid, $2, $3, $4, $5, 'public', $6, $7)
		RETURNING id::text, owner_user_id::text, name, state, trust_class, agent_version, os, hostname,
		          enrolled_at, last_seen_at`
	err = tx.QueryRow(ctx, insertWorker, ownerID, workerName, hostname, osName, agentVersion,
		deviceTokenID, deviceHash).Scan(&w.ID, &w.OwnerUserID, &w.Name, &w.State, &w.TrustClass,
		&w.AgentVer, &w.OS, &w.Hostname, &w.EnrolledAt, &w.LastSeenAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Worker{}, ErrNameTaken
		}
		return Worker{}, fmt.Errorf("store: insert self worker: %w", err)
	}

	gpus, _ := json.Marshal(caps.GPUs)
	disks, _ := json.Marshal(caps.Disks)
	netw, _ := json.Marshal(caps.Network)
	if caps.GPUs == nil {
		gpus = []byte("[]")
	}
	if caps.Disks == nil {
		disks = []byte("[]")
	}
	if caps.Network == nil {
		netw = []byte("{}")
	}
	const insertCaps = `
		INSERT INTO worker_capabilities (worker_id, cpu_model, cpu_cores, ram_bytes, gpu_json, disk_json,
			cuda_version, driver_version, network_json)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9::jsonb)`
	if _, err := tx.Exec(ctx, insertCaps, w.ID, caps.CPUModel, caps.CPUCores, caps.RAMBytes,
		gpus, disks, caps.CUDAVersion, caps.DriverVersion, netw); err != nil {
		return Worker{}, fmt.Errorf("store: insert self capabilities: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Worker{}, fmt.Errorf("store: commit: %w", err)
	}
	return w, nil
}

// WorkerByDeviceTokenID загружает узел по идентификатору секрета устройства.
func (s *Store) WorkerByDeviceTokenID(ctx context.Context, tokenID string) (Worker, error) {
	const q = `
		SELECT id::text, owner_user_id::text, name, state, trust_class, agent_version, os, hostname,
		       enrolled_at, last_seen_at, device_token_id, device_token_hash
		FROM worker_nodes WHERE device_token_id = $1`

	var w Worker
	err := s.pool.QueryRow(ctx, q, tokenID).Scan(&w.ID, &w.OwnerUserID, &w.Name, &w.State, &w.TrustClass,
		&w.AgentVer, &w.OS, &w.Hostname, &w.EnrolledAt, &w.LastSeenAt, &w.TokenID, &w.TokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, fmt.Errorf("store: worker by token: %w", err)
	}
	return w, nil
}

// RecordHeartbeat сохраняет телеметрию и обновляет last_seen_at.
func (s *Store) RecordHeartbeat(ctx context.Context, hb Heartbeat) error {
	meta := hb.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, _ := json.Marshal(meta)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertHB = `
		INSERT INTO worker_heartbeats (worker_id, state, cpu_percent, ram_free_bytes, vram_free_bytes,
			disk_free_bytes, temperature_c, power_watts, active_jobs, meta_json)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`
	if _, err := tx.Exec(ctx, insertHB, hb.WorkerID, hb.State, hb.CPUPercent, hb.RAMFreeBytes, hb.VRAMFreeBytes,
		hb.DiskFreeBytes, hb.TemperatureC, hb.PowerWatts, hb.ActiveJobs, metaJSON); err != nil {
		return fmt.Errorf("store: insert heartbeat: %w", err)
	}

	// RTT приходит в метаданных heartbeat и нужен Scheduler для прогноза скорости.
	var rtt *int
	if raw, ok := meta["rtt_ms"]; ok {
		if value, ok := toInt(raw); ok && value >= 0 {
			rtt = &value
		}
	}

	// Модель, которую узел готов выполнять: по ней выдаются задания.
	servedModel := ""
	if raw, ok := meta["served_model"]; ok {
		if value, ok := raw.(string); ok {
			servedModel = strings.TrimSpace(value)
			if len(servedModel) > 120 {
				servedModel = servedModel[:120]
			}
		}
	}

	// Окно контекста узла: панель и API показывают предел промпта.
	// Приходит из того же heartbeat, что и модель, поэтому обновляется вместе с ней.
	numCtx := 0
	if raw, ok := meta["num_ctx"]; ok {
		if value, ok := toInt(raw); ok && value > 0 && value <= 4_000_000 {
			numCtx = value
		}
	}

	// Digest'ы весов из heartbeat: узел перечисляет, что держит локально.
	var modelDigests map[string]string
	if raw, ok := meta["model_digests"]; ok {
		if values, ok := raw.(map[string]any); ok {
			modelDigests = make(map[string]string, len(values))
			for model, digest := range values {
				if text, ok := digest.(string); ok && strings.TrimSpace(model) != "" {
					modelDigests[strings.TrimSpace(model)] = text
				}
			}
		}
	}

	// Модель эмбеддингов объявляется отдельно от чат-модели.
	embeddingModel := ""
	if raw, ok := meta["served_embedding_model"]; ok {
		if value, ok := raw.(string); ok {
			embeddingModel = strings.TrimSpace(value)
			if len(embeddingModel) > 120 {
				embeddingModel = embeddingModel[:120]
			}
		}
	}

	// Адрес RPC-стадии: узел сообщает, где его ggml-rpc-server слушает.
	// Без этого координатор шард-группы не знает, куда подключаться, и
	// раскладка слоёв остаётся ручной настройкой каждого узла.
	meshRPCAddr := ""
	if raw, ok := meta["mesh_rpc_addr"]; ok {
		if value, ok := raw.(string); ok {
			meshRPCAddr = strings.TrimSpace(value)
			if len(meshRPCAddr) > 80 {
				meshRPCAddr = meshRPCAddr[:80]
			}
		}
	}

	const touch = `UPDATE worker_nodes SET last_seen_at = now(),
		rtt_ms = COALESCE($2, rtt_ms),
		served_model = $3,
		num_ctx = $4,
		served_embedding_model = $5,
		mesh_rpc_addr = $6,
		link_measured_at = CASE WHEN $2 IS NULL THEN link_measured_at ELSE now() END
		WHERE id = $1::uuid AND (state NOT IN ('maintenance', 'quarantined'))`
	if _, err := tx.Exec(ctx, touch, hb.WorkerID, rtt, servedModel, numCtx, embeddingModel, meshRPCAddr); err != nil {
		return fmt.Errorf("store: touch worker: %w", err)
	}

	// Самопроверка узла: он пересчитал sha256 файла весов и сверил с digest.
	selfVerified := map[string]bool{}
	if raw, ok := meta["model_self_verified"]; ok {
		if values, ok := raw.(map[string]any); ok {
			for model, flag := range values {
				if value, ok := flag.(bool); ok && value {
					selfVerified[strings.TrimSpace(model)] = true
				}
			}
		}
	}

	if len(modelDigests) > 0 {
		for model, digest := range modelDigests {
			if _, err := tx.Exec(ctx,
				`INSERT INTO worker_model_digests (worker_id, model, digest, self_verified) VALUES ($1::uuid, $2, $3, $4)
				 ON CONFLICT (worker_id, model) DO UPDATE
				   SET digest = EXCLUDED.digest, self_verified = EXCLUDED.self_verified, observed_at = now()`,
				hb.WorkerID, model, NormalizeDigest(digest), selfVerified[model]); err != nil {
				return fmt.Errorf("store: save model digest: %w", err)
			}
		}
	}

	return tx.Commit(ctx)
}

// toInt приводит значение из JSON к int.
func toInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case int64:
		return int(value), true
	default:
		return 0, false
	}
}

// SetWorkerState меняет состояние узла (available/paused/drain/maintenance/quarantined).
func (s *Store) SetWorkerState(ctx context.Context, workerID, state string) error {
	const q = `UPDATE worker_nodes SET state = $2 WHERE id = $1::uuid`
	tag, err := s.pool.Exec(ctx, q, workerID, state)
	if err != nil {
		return fmt.Errorf("store: set worker state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetOwnWorkerState меняет паузу только на машине этого человека.
func (s *Store) SetOwnWorkerState(ctx context.Context, ownerID, workerID, state string) error {
	if state != "available" && state != "paused" {
		return fmt.Errorf("store: participants may only pause or resume")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE worker_nodes SET state = $3 WHERE id = $1::uuid AND owner_user_id = $2::uuid`,
		workerID, ownerID, state)
	if err != nil {
		return fmt.Errorf("store: set own worker state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorkerTrustClass меняет класс доверия узла.
//
// Класс отвечает на вопрос «насколько этой машине можно доверять»: private —
// своя машина, trusted — проверенный участник, public — любой подключившийся.
// Политики поверх класса (кого пускать к каким моделям) появятся позже, но
// сам класс уже виден и управляется.
func (s *Store) SetWorkerTrustClass(ctx context.Context, workerID, class string) error {
	switch class {
	case "private", "trusted", "public":
	default:
		return fmt.Errorf("store: unknown trust class %q", class)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE worker_nodes SET trust_class = $2 WHERE id = $1::uuid`, workerID, class)
	if err != nil {
		return fmt.Errorf("store: set trust class: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorker удаляет узел вместе с историей heartbeat и заданиями-ссылками.
// Нужен, чтобы тестовые узлы не оставались в каталоге навсегда.
func (s *Store) DeleteWorker(ctx context.Context, workerID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM worker_nodes WHERE id = $1::uuid`, workerID)
	if err != nil {
		return fmt.Errorf("store: delete worker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkers выдаёт узлы с последним heartbeat и сводкой возможностей.
func (s *Store) ListWorkers(ctx context.Context) ([]WorkerListRow, error) {
	return s.listWorkers(ctx, "")
}

// ListWorkersByOwner выдаёт только машины одного аккаунта.
func (s *Store) ListWorkersByOwner(ctx context.Context, ownerID string) ([]WorkerListRow, error) {
	return s.listWorkers(ctx, ownerID)
}

func (s *Store) listWorkers(ctx context.Context, ownerID string) ([]WorkerListRow, error) {
	const q = `
		SELECT w.id::text, w.owner_user_id::text, w.name, w.state, w.trust_class, w.agent_version,
		       w.os, w.hostname, w.enrolled_at, w.last_seen_at, w.served_model, w.num_ctx, w.served_embedding_model,
		       c.cpu_cores, c.ram_bytes, c.gpu_json,
		       h.state, h.observed_at, h.disk_free_bytes, h.active_jobs,
		       h.vram_free_bytes, h.temperature_c, h.cpu_percent,
		       w.rtt_ms, c.bench_tokens_per_second, c.bench_model_billions, c.bench_at,
		       COALESCE(j.done, 0), COALESCE(j.failed, 0), j.last_tps
		FROM worker_nodes w
		LEFT JOIN worker_capabilities c ON c.worker_id = w.id
		LEFT JOIN LATERAL (
		    SELECT state, observed_at, disk_free_bytes, active_jobs,
		           vram_free_bytes, temperature_c, cpu_percent
		    FROM worker_heartbeats hb WHERE hb.worker_id = w.id
		    ORDER BY observed_at DESC LIMIT 1
		) h ON true
		LEFT JOIN LATERAL (
		    SELECT count(*) FILTER (WHERE state = 'done')   AS done,
		           count(*) FILTER (WHERE state = 'failed') AS failed,
		           (SELECT round(completion_tokens * 1000.0 / eval_ms, 1)
		              FROM jobs
		             WHERE worker_id = w.id AND eval_ms > 0 AND completion_tokens > 0
		             ORDER BY created_at DESC LIMIT 1) AS last_tps
		    FROM jobs WHERE worker_id = w.id
		) j ON true
		WHERE ($1 = '' OR w.owner_user_id::text = $1)
		ORDER BY w.created_at DESC`

	rows, err := s.pool.Query(ctx, q, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: list workers: %w", err)
	}
	defer rows.Close()

	out := make([]WorkerListRow, 0, 8)
	for rows.Next() {
		var r WorkerListRow
		var gpuJSON []byte
		if err := rows.Scan(&r.ID, &r.OwnerUserID, &r.Name, &r.State, &r.TrustClass, &r.AgentVer,
			&r.OS, &r.Hostname, &r.EnrolledAt, &r.LastSeenAt, &r.ServedModel, &r.NumCtx, &r.ServedEmbeddingModel, &r.CPUCores, &r.RAMBytes, &gpuJSON,
			&r.LastHBState, &r.LastHBAt, &r.DiskFreeBytes, &r.ActiveJobs,
			&r.VRAMFreeBytes, &r.TemperatureC, &r.CPUPercent,
			&r.RTTMS, &r.BenchTokensPS, &r.BenchBillions, &r.BenchAt,
			&r.JobsDone, &r.JobsFailed, &r.LastTokensPerSec); err != nil {
			return nil, fmt.Errorf("store: scan worker: %w", err)
		}
		if len(gpuJSON) > 0 {
			var gpus []GPU
			if json.Unmarshal(gpuJSON, &gpus) == nil {
				r.GPUCount = len(gpus)
				for _, g := range gpus {
					r.GPUVRAMBytes += g.VRAMBytes
				}
			}
		}
		// Нормированная скорость узла: мс на 1 млрд параметров по калибровке.
		if r.BenchTokensPS != nil && r.BenchBillions != nil && *r.BenchTokensPS > 0 && *r.BenchBillions > 0 {
			ms := (1000 / float64(*r.BenchTokensPS)) / float64(*r.BenchBillions)
			r.MSPerBillion = &ms
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
