package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/private-compute/network/internal/auth"
	"github.com/private-compute/network/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Настройки входа в панель.
const (
	// sessionTTL — сколько живёт вход. Неделя: панель нужна ежедневно, а
	// отзыв делается одним нажатием в «Настройках».
	sessionTTL = 7 * 24 * time.Hour
	// passwordMinLen — минимальная длина пароля панели.
	passwordMinLen = 10
	// bcryptCost — стоимость хеширования пароля.
	bcryptCost = 12
	// loginWindow и loginAttempts ограничивают перебор паролей: не больше
	// десяти неудач за десять минут с одного адреса.
	loginWindow   = 10 * time.Minute
	loginAttempts = 10
)

// scopesForRole — набор прав сессии панели.
//
// У администратора полный доступ к управлению сетью, у обычного участника —
// чат, каталог моделей и своя статистика. Ключи узлов и топологии остаются
// административными: это операции над чужим железом.
func scopesForRole(role string) []string {
	if role == "admin" {
		return []string{"admin", "chat:write", "models:read", "models:write", "usage:read", "nodes:write"}
	}
	return []string{"chat:write", "models:read", "usage:read"}
}

// loginLimiter считает неудачные попытки входа в памяти процесса.
//
// Полноценная защита от перебора требует общего хранилища, но и без него
// перебор через нашу панель должен упираться в потолок. Состояние теряется при
// перезапуске — это осознанно: перезапуск сервиса не подарок атакующему.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginEntry
}

type loginEntry struct {
	failures int
	resetAt  time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]loginEntry, 16)}
}

// blocked сообщает, исчерпан ли лимит для ключа (адрес + email).
func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return false
	}
	if time.Now().After(entry.resetAt) {
		delete(l.entries, key)
		return false
	}
	return entry.failures >= loginAttempts
}

// fail записывает неудачную попытку и возвращает, сколько осталось до блокировки.
func (l *loginLimiter) fail(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok || time.Now().After(entry.resetAt) {
		entry = loginEntry{resetAt: time.Now().Add(loginWindow)}
	}
	entry.failures++
	l.entries[key] = entry
	if len(l.entries) > 4096 {
		// Дешёвая уборка: старые записи не должны расти бесконечно.
		for k, v := range l.entries {
			if time.Now().After(v.resetAt) {
				delete(l.entries, k)
			}
		}
	}
	remaining := loginAttempts - entry.failures
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// reset очищает счётчик после успешного входа.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// registerAuthRoutes подключает вход в панель.
func (cfg Config) registerAuthRoutes(mux *http.ServeMux) {
	limiter := newLoginLimiter()

	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		email := strings.TrimSpace(strings.ToLower(body.Email))
		if email == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
			return
		}

		// Лимит считается по адресу и адресу почты: перебор одного аккаунта
		// с одного адреса упирается быстро, честный вход не страдает.
		limitKey := clientIP(r) + "|" + email
		if limiter.blocked(limitKey) {
			writeError(w, http.StatusTooManyRequests, "too_many_attempts",
				"too many failed sign-in attempts; try again later")
			return
		}

		account, err := cfg.Store.AccountByEmail(r.Context(), email)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				limiter.fail(limitKey)
				// Одинаковый ответ на «нет такого» и «неверный пароль»: иначе
				// по ответам перечисляются аккаунты.
				writeError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "auth backend unavailable")
			return
		}
		if account.PasswordHash == "" {
			writeError(w, http.StatusForbidden, "password_not_set",
				"this account has no password yet; an administrator must set one")
			return
		}
		if account.Status != "active" {
			writeError(w, http.StatusForbidden, "forbidden", "account is not active")
			return
		}
		if err := comparePassword(account.PasswordHash, body.Password); err != nil {
			remaining := limiter.fail(limitKey)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]any{
					"code":    "invalid_credentials",
					"message": "wrong email or password",
				},
				"attempts_left": remaining,
			})
			return
		}
		limiter.reset(limitKey)

		session, token, err := cfg.newSession(r, account)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      token,
			"expires_at": session.ExpiresAt,
			"user":       accountPayload(account, session),
		})
	})

	mux.Handle("GET /v1/auth/me", cfg.requireAuth("", func(w http.ResponseWriter, r *http.Request, p principal) {
		account, err := cfg.Store.AccountByID(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot read account")
			return
		}
		payload := map[string]any{
			"id":      account.ID,
			"email":   account.Email,
			"name":    account.Name,
			"role":    account.Role,
			"status":  account.Status,
			"scopes":  p.Scopes,
			"session": p.SessionKeyID,
		}
		if p.SessionKeyID != "" {
			if sessions, err := cfg.Store.SessionsByUser(r.Context(), p.UserID); err == nil {
				for _, s := range sessions {
					if s.KeyID == p.SessionKeyID {
						payload["expires_at"] = s.ExpiresAt
						break
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, payload)
	}))

	mux.Handle("POST /v1/auth/logout", cfg.requireAuth("", func(w http.ResponseWriter, r *http.Request, p principal) {
		if p.SessionKeyID == "" {
			// Выход из программы по API-ключу: завершать нечего, ключ отзывает
			// администратор.
			writeJSON(w, http.StatusOK, map[string]any{"revoked": false, "kind": "api_key"})
			return
		}
		if err := cfg.Store.RevokeSessionByKeyID(r.Context(), p.UserID, p.SessionKeyID); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal", "cannot end session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "kind": "session"})
	}))

	mux.Handle("GET /v1/auth/sessions", cfg.requireAuth("", func(w http.ResponseWriter, r *http.Request, p principal) {
		sessions, err := cfg.Store.SessionsByUser(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list sessions")
			return
		}
		out := make([]map[string]any, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, map[string]any{
				"id":           s.ID,
				"name":         s.Name,
				"user_agent":   s.UserAgent,
				"created_at":   s.CreatedAt,
				"last_used_at": s.LastUsedAt,
				"expires_at":   s.ExpiresAt,
				"revoked_at":   s.RevokedAt,
				"current":      s.KeyID == p.SessionKeyID,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
	}))

	mux.Handle("DELETE /v1/auth/sessions/{id}", cfg.requireAuth("", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "session id is required")
			return
		}
		sessions, err := cfg.Store.SessionsByUser(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list sessions")
			return
		}
		mine := false
		for _, s := range sessions {
			if s.ID == id {
				mine = true
				break
			}
		}
		if !mine {
			// Чужую сессию не отзываем: отвечаем так же, как на несуществующую.
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err := cfg.Store.RevokeSession(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "session already ended")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot end session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	}))

	// Смена своего пароля. Все сессии отзываются — в том числе текущая, поэтому
	// ответ содержит новую: панель не должна выкидывать человека на вход.
	mux.Handle("POST /v1/auth/password", cfg.requireAuth("", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if problem := passwordProblem(body.NewPassword); problem != "" {
			writeError(w, http.StatusBadRequest, "weak_password", problem)
			return
		}
		account, err := cfg.Store.AccountByID(r.Context(), p.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot read account")
			return
		}
		if account.PasswordHash != "" {
			if err := comparePassword(account.PasswordHash, body.CurrentPassword); err != nil {
				writeError(w, http.StatusForbidden, "invalid_credentials", "current password is wrong")
				return
			}
		}
		hash, err := hashPassword(body.NewPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot hash password")
			return
		}
		if err := cfg.Store.SetPassword(r.Context(), p.UserID, hash); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot save password")
			return
		}
		session, token, err := cfg.newSession(r, account)
		if err != nil {
			// Пароль сменён, сессии отозваны: человек войдёт заново.
			writeJSON(w, http.StatusOK, map[string]any{"changed": true, "token": ""})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"changed":    true,
			"token":      token,
			"expires_at": session.ExpiresAt,
			"user":       accountPayload(account, session),
		})
	}))

	// Управление аккаунтами: администратор заводит людей и задаёт пароли.
	mux.Handle("GET /admin/v1/users", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		accounts, err := cfg.Store.ListAccounts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list accounts")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": accounts})
	}))

	mux.Handle("POST /admin/v1/users/{id}/password", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		var body struct {
			Password string `json:"password"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if problem := passwordProblem(body.Password); problem != "" {
			writeError(w, http.StatusBadRequest, "weak_password", problem)
			return
		}
		if _, err := cfg.Store.AccountByID(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "account not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot read account")
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot hash password")
			return
		}
		if err := cfg.Store.SetPassword(r.Context(), id, hash); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot save password")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": id})
	}))

	// Закрыть доступ: аккаунт помечается удалённым, сессии отзываются, ключи
	// перестают работать. Строка остаётся, чтобы не потерять историю заданий.
	mux.Handle("DELETE /admin/v1/users/{id}", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "user id is required")
			return
		}
		if id == p.UserID {
			// Иначе администратор может закрыть доступ сам себе и остаться без
			// панели до вмешательства на сервере.
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot delete your own account")
			return
		}
		if err := cfg.Store.DisableAccount(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "account not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot delete account")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	}))
}

// newSession выпускает токен сессии и сохраняет её хеш.
func (cfg Config) newSession(r *http.Request, account store.Account) (store.Session, string, error) {
	token, err := auth.NewSessionToken()
	if err != nil {
		return store.Session{}, "", err
	}
	name := sessionName(r.UserAgent())
	session, err := cfg.Store.CreateSession(r.Context(), account.ID, token.KeyID,
		auth.Hash(cfg.Pepper, token.Secret), name, scopesForRole(account.Role),
		truncate(r.UserAgent(), 160), time.Now().Add(sessionTTL))
	if err != nil {
		return store.Session{}, "", err
	}
	return session, token.String(), nil
}

// accountPayload — то, что видит панель про своего пользователя.
func accountPayload(account store.Account, session store.Session) map[string]any {
	return map[string]any{
		"id":         account.ID,
		"email":      account.Email,
		"name":       account.Name,
		"role":       account.Role,
		"status":     account.Status,
		"scopes":     session.Scopes,
		"expires_at": session.ExpiresAt,
	}
}

// hashPassword считает bcrypt-хеш пароля.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// comparePassword проверяет пароль против хеша за постоянное время.
func comparePassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// passwordProblem проверяет новый пароль и возвращает текст проблемы.
func passwordProblem(password string) string {
	if len([]rune(password)) < passwordMinLen {
		return "password must be at least 10 characters"
	}
	if strings.TrimSpace(password) == "" {
		return "password must not be blank"
	}
	return ""
}

// sessionName — короткое имя входа для списка сессий.
func sessionName(userAgent string) string {
	agent := strings.ToLower(userAgent)
	switch {
	case strings.Contains(agent, "edg"):
		return "Edge"
	case strings.Contains(agent, "chrome"):
		return "Chrome"
	case strings.Contains(agent, "firefox"):
		return "Firefox"
	case strings.Contains(agent, "safari"):
		return "Safari"
	case strings.Contains(agent, "curl"):
		return "curl"
	case agent == "":
		return "unknown client"
	default:
		return truncate(userAgent, 40)
	}
}

// truncate обрезает строку до предела, не разрывая UTF-8 посередине руны.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// authorizeSession проверяет токен сессии и достаёт владельца.
func (cfg Config) authorizeSession(w http.ResponseWriter, r *http.Request, token auth.Token) (principal, bool) {
	session, err := cfg.Store.SessionByKeyID(r.Context(), token.KeyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid session")
			return principal{}, false
		}
		writeError(w, http.StatusInternalServerError, "internal", "auth backend unavailable")
		return principal{}, false
	}
	if !auth.Equal(auth.Hash(cfg.Pepper, token.Secret), session.KeyHash) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid session")
		return principal{}, false
	}
	if session.RevokedAt != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session ended")
		return principal{}, false
	}
	if session.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session expired")
		return principal{}, false
	}
	if session.UserStatus != "active" {
		writeError(w, http.StatusForbidden, "forbidden", "account is not active")
		return principal{}, false
	}

	go func(sessionDBID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := cfg.Store.TouchSession(ctx, sessionDBID); err != nil && cfg.Log != nil {
			cfg.Log.Warn("touch session failed", "err", err)
		}
	}(session.ID)

	return principal{
		UserID:       session.UserID,
		Scopes:       session.Scopes,
		SessionKeyID: session.KeyID,
		SessionDBID:  session.ID,
	}, true
}
