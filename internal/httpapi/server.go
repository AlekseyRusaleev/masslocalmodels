// Package httpapi — транспортный слой API Gateway.
//
// Публичные endpoints этапа 1:
//
//	GET  /health, /v1/health   — состояние процесса и зависимостей
//	GET  /v1/models            — каталог логических моделей (scope models:read)
//	GET  /v1/usage             — статистика своего аккаунта (scope usage:read)
//
// Административные endpoints (scope admin):
//
//	POST   /admin/v1/users
//	POST   /admin/v1/keys      — секрет возвращается один раз
//	GET    /admin/v1/keys?user_id=
//	DELETE /admin/v1/keys/{id}
//
// Журналирование не пишет тела запросов, заголовок Authorization и содержимое
// сообщений: только метод, путь, статус, длительность и request ID.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/private-compute/network/internal/auth"
	"github.com/private-compute/network/internal/store"
)

// Config — настройки API Gateway.
type Config struct {
	Addr     string
	Version  string
	Pepper   []byte
	NatsURL  string
	Store    *store.Store
	Log      *slog.Logger
	MaxItems int
	// Hub связывает ожидающие стримы с чанками узлов.
	Hub *hub
	// StartedAt — время запуска процесса. Нужно /metrics, чтобы время работы
	// было измеренным, а не выдуманным.
	StartedAt time.Time
	// RateLimiter ограничивает частоту запросов инференса на аккаунт.
	// nil означает «без ограничения».
	RateLimiter *rateLimiter
	// EnrollLimiter ограничивает самозапись узлов по адресу.
	EnrollLimiter *rateLimiter
	// ControlURL — публичный адрес платформы, который узел получает при
	// самозаписи (чтобы участнику не нужно было знать его заранее).
	ControlURL string
	// JoinCode — общий код сети. Без него самозапись закрыта.
	JoinCode string
	// AllowOpenEnroll разрешает самозапись без кода. Только для лаборатории:
	// включается переменной ALLOW_OPEN_ENROLL=1, в обычной сети остаётся выключенным.
	AllowOpenEnroll bool
}

const (
	requestIDHeader = "X-Request-Id"
	dialTimeout     = 800 * time.Millisecond
	maxBodyBytes    = 64 << 10
)

type principal struct {
	UserID string
	KeyDB  string
	Scopes []string

	// SessionKeyID и SessionDBID заполнены, когда запрос пришёл из панели по
	// сессии. Тогда выход и смена пароля относятся к конкретному входу.
	SessionKeyID string
	SessionDBID  string
}

type principalKey struct{}

// New собирает HTTP-обработчик со всеми маршрутами этапа 1.
func New(cfg Config) http.Handler {
	logger := cfg.Log
	if logger == nil {
		logger = slog.Default()
	}
	// Один hub на процесс: связывает HTTP-стримы клиентов и чанки узлов.
	if cfg.Hub == nil {
		cfg.Hub = newHub()
	}
	// Аренда заданий: раз в 15 секунд возвращаем в очередь то, чей узел замолчал.
	go cfg.runJobReaper(cfg.Hub)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"version":     cfg.Version,
			"control_url": cfg.ControlURL,
			"time":        time.Now().UTC().Format(time.RFC3339),
			"checks": map[string]string{
				"database": dbCheck(r.Context(), cfg.Store),
				"nats":     tcpCheck(cfg.NatsURL, "4222"),
			},
		})
	})

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": cfg.Version,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Каталог моделей: то, что действительно можно вызвать.
	// Берём модели, о которых отчитались живые узлы за последние 3 минуты, и
	// добавляем активные ревизии каталога. Пустой список честнее выдуманного.
	mux.Handle("GET /v1/models", cfg.requireAuth("models:read", func(w http.ResponseWriter, r *http.Request, _ principal) {
		serving, err := cfg.Store.ServingModels(r.Context(), servingWindow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list models")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   serving,
		})
	}))

	// Одна модель по идентификатору: некоторые клиенты и SDK сначала спрашивают
	// именно её, а не список.
	mux.Handle("GET /v1/models/{id}", cfg.requireAuth("models:read", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		serving, err := cfg.Store.ServingModels(r.Context(), servingWindow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list models")
			return
		}
		for _, model := range serving {
			if model.ID == id {
				writeJSON(w, http.StatusOK, model)
				return
			}
		}
		writeError(w, http.StatusNotFound, "model_not_served",
			"model "+id+" is not served by any online worker"+availableSuffix(r, cfg, id))
	}))

	// Своя статистика: без чужих данных.
	mux.Handle("GET /v1/usage", cfg.requireAuth("usage:read", func(w http.ResponseWriter, r *http.Request, p principal) {
		keys, err := cfg.Store.KeysByUser(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "usage unavailable")
			return
		}
		var lastUsed *time.Time
		for _, k := range keys {
			if k.LastUsedAt != nil && (lastUsed == nil || k.LastUsedAt.After(*lastUsed)) {
				lastUsed = k.LastUsedAt
			}
		}
		// Потребление считается по журналу учёта: одна запись на задание,
		// поэтому повторная доставка результата не удваивает цифры.
		usage, err := cfg.Store.UsageByUser(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "usage unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"jobs":              usage.Jobs,
			"prompt_tokens":     usage.PromptTokens,
			"completion_tokens": usage.CompletionTokens,
			"eval_ms":           usage.EvalMS,
			"api_keys":          len(keys),
			"last_key_used":     lastUsed,
		})
	}))

	// Административные endpoints.
	// Создание аккаунта: имя, роль и пароль можно задать сразу — тогда человек
	// входит в панель без отдельного шага «выдать пароль».
	mux.Handle("POST /admin/v1/users", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		var body struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			Role     string `json:"role"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !strings.Contains(body.Email, "@") {
			writeError(w, http.StatusBadRequest, "invalid_request", "email is required")
			return
		}
		if body.Role == "" {
			body.Role = "user"
		}
		if body.Role != "user" && body.Role != "admin" {
			writeError(w, http.StatusBadRequest, "invalid_request", "role must be user or admin")
			return
		}
		hash := ""
		if body.Password != "" {
			if problem := passwordProblem(body.Password); problem != "" {
				writeError(w, http.StatusBadRequest, "weak_password", problem)
				return
			}
			var err error
			if hash, err = hashPassword(body.Password); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "cannot hash password")
				return
			}
		}
		account, err := cfg.Store.UpsertAccount(r.Context(), body.Email, body.Name, body.Role, hash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create user")
			return
		}
		writeJSON(w, http.StatusCreated, account)
	}))

	mux.Handle("POST /admin/v1/keys", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			UserID string   `json:"user_id"`
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		// Панель выпускает ключ для себя же: владелец по умолчанию — администратор.
		if body.UserID == "" {
			body.UserID = p.UserID
		}
		if body.UserID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "user_id is required")
			return
		}
		if len(body.Scopes) == 0 {
			body.Scopes = []string{"models:read", "usage:read", "chat:write"}
		}

		token, err := auth.NewToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot generate token")
			return
		}
		key, err := cfg.Store.CreateKey(r.Context(), body.UserID, token.KeyID, body.Name,
			auth.Hash(cfg.Pepper, token.Secret), body.Scopes)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot store key")
			return
		}

		// Секрет показывается один раз и никогда не сохраняется в БД в открытом виде.
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

	mux.Handle("GET /admin/v1/keys", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "user_id query parameter is required")
			return
		}
		keys, err := cfg.Store.KeysByUser(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list keys")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": keys})
	}))

	mux.Handle("DELETE /admin/v1/keys/{id}", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "key id is required")
			return
		}
		if err := cfg.Store.RevokeKey(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "key not found or already revoked")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot revoke key")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	}))

	// Маршруты этапа 2: регистрация узлов, heartbeat, управление состоянием.
	cfg.registerWorkerRoutes(mux)

	// Каталог моделей и манифест артефактов для узлов.
	cfg.registerModelRoutes(mux)

	// Топологии: кто координатор и какие слои держит какой узел.
	cfg.registerTopologyRoutes(mux)

	// Туннель шард-группы: байтовый поток между координатором и стадией
	// через платформу, потому что RPC-порт наружу не выставляется.
	cfg.registerMeshLinkRoutes(mux)

	// Калибровка узла: измеренная скорость декода для прогноза.
	cfg.registerBenchmarkRoutes(mux)

	// Инференс: /v1/chat/completions и задания для узлов.
	cfg.registerInferenceRoutes(mux)

	// Вход в панель: пароль, сессии, управление аккаунтами.
	cfg.registerAuthRoutes(mux)

	// История диалогов: разговор принадлежит аккаунту и живёт на сервере.
	cfg.registerConversationRoutes(mux)

	// Самозапись узла: участнику не нужен код от владельца сети.
	cfg.registerSelfEnrollmentRoutes(mux)

	// Метрики для наблюдения: только цифры платформы, без чужих данных.
	cfg.registerMetricsRoutes(mux)

	return withRequestID(withAccessLog(logger, mux))
}

// requireAuth проверяет Bearer-ключ и scope, затем вызывает обработчик.
func (cfg Config) requireAuth(scope string, next func(http.ResponseWriter, *http.Request, principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authorization: Bearer <token> required")
			return
		}

		token, kind, err := auth.ParseAny(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "malformed token")
			return
		}

		// Панель приносит сессию, программа — ключ. Проверяются они в разных
		// таблицах, но права и ответы одинаковые.
		if kind == auth.KindSession {
			sessionPrincipal, ok := cfg.authorizeSession(w, r, token)
			if !ok {
				return
			}
			if scope != "" && !hasScope(sessionPrincipal.Scopes, scope) {
				writeError(w, http.StatusForbidden, "forbidden", "scope required: "+scope)
				return
			}
			next(w, r, sessionPrincipal)
			return
		}

		key, err := cfg.Store.KeyByKeyID(r.Context(), token.KeyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "auth backend unavailable")
			return
		}

		if !auth.Equal(auth.Hash(cfg.Pepper, token.Secret), key.KeyHash) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid token")
			return
		}
		if key.RevokedAt != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "token revoked")
			return
		}
		if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "token expired")
			return
		}
		if key.UserStatus != "active" {
			writeError(w, http.StatusForbidden, "forbidden", "account is not active")
			return
		}
		if scope != "" && !hasScope(key.Scopes, scope) {
			writeError(w, http.StatusForbidden, "forbidden", "scope required: "+scope)
			return
		}

		go func(keyDBID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := cfg.Store.TouchKey(ctx, keyDBID); err != nil && cfg.Log != nil {
				cfg.Log.Warn("touch key failed", "err", err)
			}
		}(key.ID)

		next(w, r, principal{UserID: key.UserID, KeyDB: key.ID, Scopes: key.Scopes})
	})
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONLimit(w, r, dst, maxBodyBytes)
}

// decodeJSONLimit — тот же разбор с другим пределом тела.
//
// Обычные запросы ограничены 64 КБ: больше в них не бывает. Но история диалога
// несёт длинные ответы модели вместе с размышлениями, и один ход легко
// перерастает этот предел — для таких запросов предел выше.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func dbCheck(ctx context.Context, st *store.Store) string {
	if st == nil {
		return "unconfigured"
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := st.Ping(checkCtx); err != nil {
		return "down"
	}
	return "ok"
}

func tcpCheck(rawURL, defaultPort string) string {
	if rawURL == "" {
		return "unconfigured"
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "invalid"
	}
	host := parsed.Hostname()
	if host == "" {
		host = parsed.Path
	}
	port := parsed.Port()
	if port == "" {
		port = defaultPort
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	if err != nil {
		return "down"
	}
	_ = conn.Close()
	return "ok"
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		r.Header.Set(requestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

func withAccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.written,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", r.Header.Get(requestIDHeader),
			"remote", remoteHost(r.RemoteAddr),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.written += n
	return n, err
}

// Flush пробрасывает сброс буфера в настоящий ResponseWriter.
// Без этого SSE-поток не работал: обёртка не реализовывала http.Flusher,
// и обработчик считал стрим неподдерживаемым.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap даёт http.ResponseController доступ к исходному писателю.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "req-fallback"
	}
	return hex.EncodeToString(buf)
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}

// clientIP — адрес клиента.
//
// За nginx шлюз видит 127.0.0.1. X-Real-IP принимается только от loopback:
// прямой запрос на порт шлюза не может подставить чужой адрес.
func clientIP(r *http.Request) string {
	host := remoteHost(r.RemoteAddr)
	if !isLoopbackHost(host) {
		return host
	}
	forwarded := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if comma := strings.Index(forwarded, ","); comma >= 0 {
		forwarded = strings.TrimSpace(forwarded[:comma])
	}
	ip := net.ParseIP(forwarded)
	if ip == nil {
		return host
	}
	return ip.String()
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
