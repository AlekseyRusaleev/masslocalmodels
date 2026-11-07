package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/private-compute/network/internal/auth"
	"github.com/private-compute/network/internal/store"
	"github.com/private-compute/network/internal/worker"
)

// registerSelfEnrollmentRoutes подключает самозапись узла.
//
// Смысл: участник не должен зависеть от владельца сети. Он скачивает программу,
// она сама регистрируется, получает секрет устройства и ключ API для харнеса —
// и сразу знает, какую модель скачать.
//
// Защита: лимит по адресу клиента и общий код сети. Без кода самозапись закрыта.
// Открыть её можно только явно, переменной ALLOW_OPEN_ENROLL=1.
func (cfg Config) registerSelfEnrollmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/nodes/register", func(w http.ResponseWriter, r *http.Request) {
		// Лимит по адресу: самозапись — публичный endpoint.
		if ok, retry := cfg.EnrollLimiter.allow("enroll:" + clientIP(r)); !ok {
			seconds := int(retry.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeError(w, http.StatusTooManyRequests, "rate_limited",
				"too many registrations from this address; retry in "+strconv.Itoa(seconds)+"s")
			return
		}

		var body struct {
			Name         string             `json:"name"`
			Hostname     string             `json:"hostname"`
			OS           string             `json:"os"`
			AgentVersion string             `json:"agent_version"`
			JoinCode     string             `json:"join_code"`
			Hardware     store.Capabilities `json:"hardware"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !cfg.AllowOpenEnroll && !joinCodeMatches(cfg.JoinCode, body.JoinCode) {
			message := "network join code is required: ask the network operator"
			if strings.TrimSpace(cfg.JoinCode) == "" {
				message = "self-enrollment is closed until the operator sets JOIN_CODE"
			}
			writeError(w, http.StatusForbidden, "invalid_join_code", message)
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			name = strings.TrimSpace(body.Hostname)
		}
		if name == "" || len(name) > 64 {
			writeError(w, http.StatusBadRequest, "invalid_request", "name is required and must be <= 64 chars")
			return
		}

		deviceToken, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate device token")
			return
		}
		worker, err := cfg.Store.RegisterSelfWorker(r.Context(), name, body.Hostname, body.OS,
			body.AgentVersion, deviceToken.KeyID, auth.Hash(cfg.Pepper, deviceToken.Secret), body.Hardware)
		if err != nil {
			if errors.Is(err, store.ErrNameTaken) {
				writeError(w, http.StatusConflict, "name_taken",
					"a worker with this name is already registered; choose another name")
				return
			}
			cfg.Log.Error("cannot self-register worker", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "cannot register worker")
			return
		}

		// Ключ для харнеса: только чтение каталога и чат — этого достаточно
		// участнику, и этого мало, чтобы управлять сетью.
		apiToken, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate api key")
			return
		}
		scopes := []string{"models:read", "chat:write"}
		if _, err := cfg.Store.CreateKey(r.Context(), worker.OwnerUserID, apiToken.KeyID,
			"node-harness", auth.Hash(cfg.Pepper, apiToken.Secret), scopes); err != nil {
			cfg.Log.Error("cannot create harness key", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "cannot issue api key")
			return
		}

		model := cfg.servedModelOrDefault(r)
		cfg.Log.Info("worker self-registered", "worker", worker.Name, "model", model)

		writeJSON(w, http.StatusCreated, map[string]any{
			"worker_id":     worker.ID,
			"name":          worker.Name,
			"trust_class":   worker.TrustClass,
			"device_token":  deviceToken.String(),
			"api_key":       apiToken.String(),
			"control_url":   cfg.ControlURL,
			"model":         model,
			"registered_at": time.Now().UTC(),
			"note":          "the device token is shown once: it is stored locally and protected by the OS",
		})
	})
}

// servedModelOrDefault — какую модель предложить новому узлу.
//
// Берём ту, что сеть обслуживает сейчас: бессмысленно скачивать участнику
// модель, которую никто не вызывает.
func (cfg Config) servedModelOrDefault(r *http.Request) string {
	models, err := cfg.Store.ServingModels(r.Context(), servingWindow)
	if err == nil {
		best := ""
		bestCount := 0
		for _, model := range models {
			if len(model.ServedBy) > bestCount {
				best, bestCount = model.ID, len(model.ServedBy)
			}
		}
		if best != "" && !worker.IsRetiredModel(best) {
			return best
		}
	}
	return worker.DefaultChatModel
}

// joinCodeMatches сравнивает код сети за постоянное время.
// Пустой ожидаемый код не совпадает ни с чем: самозапись без кода закрыта.
func joinCodeMatches(want, got string) bool {
	want = strings.TrimSpace(want)
	got = strings.TrimSpace(got)
	if want == "" || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
