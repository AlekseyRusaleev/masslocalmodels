package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/private-compute/network/internal/auth"
	"github.com/private-compute/network/internal/store"
)

const (
	// enrollmentTTL — срок жизни одноразового токена подключения.
	enrollmentTTL = 24 * time.Hour
	// maxHeartbeatMeta ограничивает размер метаданных heartbeat.
	maxHeartbeatMeta = 8 << 10
)

type workerPrincipal struct {
	Worker store.Worker
}

// allowedStates — состояния, которые администратор может назначить узлу.
var allowedStates = map[string]bool{
	"available":   true,
	"paused":      true,
	"drain":       true,
	"maintenance": true,
	"quarantined": true,
}

// registerWorkerRoutes подключает маршруты работы с узлами.
func (cfg Config) registerWorkerRoutes(mux *http.ServeMux) {
	// Администратор выпускает одноразовый токен подключения.
	// Если владелец не указан явно, узел привязывается к самому администратору:
	// в панели это обычный сценарий «подключить ещё одну машину».
	mux.Handle("POST /admin/v1/enrollments", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			UserID   string `json:"user_id"`
			Label    string `json:"label"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.UserID == "" {
			body.UserID = p.UserID
		}
		if body.UserID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "user_id is required")
			return
		}
		ttl := enrollmentTTL
		if body.TTLHours > 0 {
			if body.TTLHours > 24*7 {
				writeError(w, http.StatusBadRequest, "invalid_request", "ttl_hours must be <= 168")
				return
			}
			ttl = time.Duration(body.TTLHours) * time.Hour
		}

		token, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate token")
			return
		}
		enrollment, err := cfg.Store.CreateEnrollment(r.Context(), body.UserID, token.KeyID, body.Label,
			auth.Hash(cfg.Pepper, token.Secret), ttl)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot store enrollment")
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         enrollment.ID,
			"user_id":    enrollment.OwnerUserID,
			"label":      enrollment.Label,
			"expires_at": enrollment.ExpiresAt,
			"token":      token.String(),
			"warning":    "enrollment token is single-use and shown only once",
		})
	}))

	// Узел регистрируется по одноразовому токену и получает свой секрет устройства.
	mux.HandleFunc("POST /worker/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token        string             `json:"token"`
			Name         string             `json:"name"`
			Hostname     string             `json:"hostname"`
			OS           string             `json:"os"`
			AgentVersion string             `json:"agent_version"`
			Hardware     store.Capabilities `json:"hardware"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if strings.TrimSpace(body.Name) == "" || len(body.Name) > 64 {
			writeError(w, http.StatusBadRequest, "invalid_request", "name is required and must be <= 64 chars")
			return
		}

		token, err := auth.Parse(body.Token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid enrollment token")
			return
		}
		enrollment, err := cfg.Store.EnrollmentByTokenID(r.Context(), token.KeyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid enrollment token")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "enrollment backend unavailable")
			return
		}
		if !auth.Equal(auth.Hash(cfg.Pepper, token.Secret), enrollment.TokenHash) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid enrollment token")
			return
		}
		if enrollment.UsedAt != nil {
			writeError(w, http.StatusConflict, "already_used", "enrollment token already used")
			return
		}
		if enrollment.ExpiresAt.Before(time.Now()) {
			writeError(w, http.StatusGone, "expired", "enrollment token expired")
			return
		}

		deviceToken, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate device token")
			return
		}

		worker, err := cfg.Store.RegisterWorker(r.Context(), enrollment.ID, strings.TrimSpace(body.Name),
			body.Hostname, body.OS, body.AgentVersion, deviceToken.KeyID,
			auth.Hash(cfg.Pepper, deviceToken.Secret), body.Hardware)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusConflict, "already_used", "enrollment token already used")
				return
			}
			if errors.Is(err, store.ErrNameTaken) {
				// Понятный ответ вместо «internal error»: имя узла занято, нужно
				// выбрать другое или удалить старый узел в панели.
				writeError(w, http.StatusConflict, "name_taken",
					"a worker with this name is already registered; choose another name or delete the old worker")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot register worker")
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"worker_id":    worker.ID,
			"name":         worker.Name,
			"state":        worker.State,
			"trust_class":  worker.TrustClass,
			"enrolled_at":  worker.EnrolledAt,
			"device_token": deviceToken.String(),
			"warning":      "device token is shown only once; store it locally and never log it",
		})
	})

	// Heartbeat: телеметрия и получение желаемого состояния от Control Plane.
	mux.Handle("POST /worker/v1/heartbeat", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		var body struct {
			State         string         `json:"state"`
			CPUPercent    *float32       `json:"cpu_percent"`
			RAMFreeBytes  *int64         `json:"ram_free_bytes"`
			VRAMFreeBytes *int64         `json:"vram_free_bytes"`
			DiskFreeBytes *int64         `json:"disk_free_bytes"`
			TemperatureC  *float32       `json:"temperature_c"`
			PowerWatts    *float32       `json:"power_watts"`
			ActiveJobs    *int           `json:"active_jobs"`
			Meta          map[string]any `json:"meta"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if len(body.Meta) > 0 {
			if encoded, err := marshalLimited(body.Meta, maxHeartbeatMeta); err != nil || !encoded {
				writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "meta is too large")
				return
			}
		}
		if body.State != "" && !allowedStates[body.State] {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown worker state")
			return
		}

		hb := store.Heartbeat{
			WorkerID:      wp.Worker.ID,
			State:         body.State,
			CPUPercent:    body.CPUPercent,
			RAMFreeBytes:  body.RAMFreeBytes,
			VRAMFreeBytes: body.VRAMFreeBytes,
			DiskFreeBytes: body.DiskFreeBytes,
			TemperatureC:  body.TemperatureC,
			PowerWatts:    body.PowerWatts,
			ActiveJobs:    body.ActiveJobs,
			Meta:          body.Meta,
		}
		if err := cfg.Store.RecordHeartbeat(r.Context(), hb); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot record heartbeat")
			return
		}
		if capable, _ := body.Meta["mesh_capable"].(bool); capable {
			if model, _ := body.Meta["mesh_model"].(string); strings.TrimSpace(model) != "" {
				if err := cfg.Store.EnsureLayerSplit(r.Context(), model); err != nil && cfg.Log != nil {
					cfg.Log.Warn("layer split was not updated", "err", err)
				}
			}
		}

		plan, planErr := cfg.weightPlan(r.Context(), wp.Worker.ID)
		cfg.logWeightPlanError(planErr)

		reply := map[string]any{
			"ok":                         true,
			"worker_id":                  wp.Worker.ID,
			"desired_state":              wp.Worker.State,
			"server_time":                time.Now().UTC(),
			"heartbeat_interval_seconds": 30,
		}
		if plan != nil {
			reply["weights"] = plan
		}
		writeJSON(w, http.StatusOK, reply)
	}))

	// Список узлов для администратора: без секретов и путей.
	// Аттестация: какие веса держит каждый узел и совпадают ли они с манифестом.
	mux.Handle("GET /admin/v1/attestation", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		workers, err := cfg.Store.ListWorkers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list workers")
			return
		}
		type workerAttestation struct {
			WorkerID string                    `json:"worker_id"`
			Name     string                    `json:"name"`
			Models   []store.WorkerAttestation `json:"models"`
		}
		out := make([]workerAttestation, 0, len(workers))
		for _, worker := range workers {
			models, err := cfg.Store.AttestationsForWorker(r.Context(), cfg.Pepper, worker.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "cannot read attestation")
				return
			}
			out = append(out, workerAttestation{WorkerID: worker.ID, Name: worker.Name, Models: models})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
	}))

	mux.Handle("GET /admin/v1/workers", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		workers, err := cfg.Store.ListWorkers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list workers")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": workers})
	}))

	// Управление состоянием узла: пауза, drain, обслуживание, карантин.
	mux.Handle("POST /admin/v1/workers/{id}/state", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		var body struct {
			State string `json:"state"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !allowedStates[body.State] {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown worker state")
			return
		}
		if err := cfg.Store.SetWorkerState(r.Context(), id, body.State); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "worker not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot change worker state")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"worker_id": id, "state": body.State})
	}))

	// Удаление узла: убирает тестовые и списанные машины из каталога.
	// Активные задания узла остаются в истории с пустой ссылкой на узел.
	// Класс доверия узла: private (своя машина), trusted (проверенный
	// участник), public (любой подключившийся).
	mux.Handle("POST /admin/v1/workers/{id}/trust", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		var body struct {
			TrustClass string `json:"trust_class"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := cfg.Store.SetWorkerTrustClass(r.Context(), id, body.TrustClass); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "worker not found")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"worker_id": id, "trust_class": body.TrustClass})
	}))

	mux.Handle("DELETE /admin/v1/workers/{id}", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		if err := cfg.Store.DeleteWorker(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "worker not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot delete worker")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"worker_id": id, "deleted": true})
	}))

	// Участник видит и ставит на паузу только свои машины.
	mux.Handle("GET /v1/nodes", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		workers, err := cfg.Store.ListWorkersByOwner(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list workers")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": workers})
	}))

	mux.Handle("POST /v1/nodes/{id}/state", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			State string `json:"state"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := cfg.Store.SetOwnWorkerState(r.Context(), p.UserID, r.PathValue("id"), body.State); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "worker not found")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"worker_id": r.PathValue("id"), "state": body.State})
	}))

	mux.Handle("POST /v1/enrollments", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			Label    string `json:"label"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		ttl := enrollmentTTL
		if body.TTLHours > 0 {
			if body.TTLHours > 24*7 {
				writeError(w, http.StatusBadRequest, "invalid_request", "ttl_hours must be <= 168")
				return
			}
			ttl = time.Duration(body.TTLHours) * time.Hour
		}
		token, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate token")
			return
		}
		enrollment, err := cfg.Store.CreateEnrollment(r.Context(), p.UserID, token.KeyID, body.Label,
			auth.Hash(cfg.Pepper, token.Secret), ttl)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot store enrollment")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         enrollment.ID,
			"user_id":    enrollment.OwnerUserID,
			"label":      enrollment.Label,
			"expires_at": enrollment.ExpiresAt,
			"token":      token.String(),
			"warning":    "enrollment token is single-use and shown only once",
		})
	}))

	mux.Handle("POST /v1/keys", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			name = "харнес"
		}
		token, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate token")
			return
		}
		scopes := []string{"models:read", "chat:write"}
		key, err := cfg.Store.CreateKey(r.Context(), p.UserID, token.KeyID, name,
			auth.Hash(cfg.Pepper, token.Secret), scopes)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot store key")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         key.ID,
			"user_id":    key.UserID,
			"name":       key.Name,
			"scopes":     key.Scopes,
			"created_at": key.CreatedAt,
			"token":      token.String(),
			"warning":    "token is shown only once",
		})
	}))
}

// requireWorker проверяет секрет устройства и передаёт узел в обработчик.
func (cfg Config) requireWorker(next func(http.ResponseWriter, *http.Request, workerPrincipal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="worker"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "device token required")
			return
		}
		token, err := auth.Parse(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "malformed token")
			return
		}
		worker, err := cfg.Store.WorkerByDeviceTokenID(r.Context(), token.KeyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device token")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "worker backend unavailable")
			return
		}
		if !auth.Equal(auth.Hash(cfg.Pepper, token.Secret), worker.TokenHash) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device token")
			return
		}
		if worker.State == "quarantined" {
			writeError(w, http.StatusForbidden, "quarantined", "worker is quarantined")
			return
		}
		next(w, r, workerPrincipal{Worker: worker})
	})
}

// marshalLimited проверяет, что метаданные укладываются в лимит.
func marshalLimited(v any, limit int) (bool, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return false, err
	}
	return len(data) <= limit, nil
}
