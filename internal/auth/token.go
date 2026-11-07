// Package auth — выпуск и проверка API-ключей.
//
// Формат ключа: "dai_<key_id>.<secret>", где key_id — публичный префикс для поиска
// строки в БД, secret — 32 случайных байта в base64url. В базе хранится только
// HMAC-SHA256(pepper, secret), поэтому по украденному дампу БД ключи не восстановить.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// TokenPrefix отделяет наши ключи от чужих форматов в логах и заголовках.
	TokenPrefix = "dai_"
	// SessionPrefix — токен сессии панели. Формат тот же, но проверяется он по
	// таблице auth_sessions: у сессии свой срок жизни и свой отзыв.
	SessionPrefix = "ses_"
	keyIDBytes    = 9  // 12 символов base64url
	secretBytes   = 32 // 43 символа base64url
)

// Kind — где проверять секрет: в ключах API или в сессиях панели.
type Kind int

const (
	// KindAPIKey — постоянный ключ программы.
	KindAPIKey Kind = iota
	// KindSession — временный вход в панель.
	KindSession
)

var (
	ErrMalformed = errors.New("auth: malformed token")
	ErrEmpty     = errors.New("auth: empty token")
)

// Token — разобранное представление ключа или сессии.
type Token struct {
	KeyID  string
	Secret string

	// prefix помнит, откуда токен: dai_ или ses_. Нужен, чтобы String()
	// возвращал ровно то, что потом примут.
	prefix string
}

// String собирает строковое представление токена.
func (t Token) String() string {
	prefix := t.prefix
	if prefix == "" {
		prefix = TokenPrefix
	}
	return prefix + t.KeyID + "." + t.Secret
}

// NewToken выпускает новый случайный ключ.
func NewToken() (Token, error) { return newToken(TokenPrefix) }

// NewSessionToken выпускает новый токен сессии панели.
func NewSessionToken() (Token, error) { return newToken(SessionPrefix) }

// newToken создаёт пару «публичный идентификатор + секрет» с заданным префиксом.
func newToken(prefix string) (Token, error) {
	idRaw := make([]byte, keyIDBytes)
	if _, err := rand.Read(idRaw); err != nil {
		return Token{}, err
	}
	secretRaw := make([]byte, secretBytes)
	if _, err := rand.Read(secretRaw); err != nil {
		return Token{}, err
	}
	return Token{
		KeyID:  base64.RawURLEncoding.EncodeToString(idRaw),
		Secret: base64.RawURLEncoding.EncodeToString(secretRaw),
		prefix: prefix,
	}, nil
}

// Parse проверяет формат ключа. Секрет не сравнивается с БД — это делает store.
func Parse(raw string) (Token, error) {
	token, kind, err := ParseAny(raw)
	if err != nil {
		return Token{}, err
	}
	if kind != KindAPIKey {
		return Token{}, ErrMalformed
	}
	return token, nil
}

// ParseAny разбирает и API-ключ, и токен сессии, сообщая, где его проверять.
//
// Один разбор на оба типа нужен обработчику запроса: панель приносит сессию,
// программа — ключ, и до разбора их не различить.
func ParseAny(raw string) (Token, Kind, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Token{}, KindAPIKey, ErrEmpty
	}

	prefix, kind := TokenPrefix, KindAPIKey
	if strings.HasPrefix(raw, SessionPrefix) {
		prefix, kind = SessionPrefix, KindSession
	} else if !strings.HasPrefix(raw, TokenPrefix) {
		return Token{}, KindAPIKey, ErrMalformed
	}

	rest := strings.TrimPrefix(raw, prefix)
	keyID, secret, ok := strings.Cut(rest, ".")
	if !ok || keyID == "" || secret == "" {
		return Token{}, kind, ErrMalformed
	}
	if len(keyID) > 32 || len(secret) > 64 {
		return Token{}, kind, ErrMalformed
	}
	return Token{KeyID: keyID, Secret: secret, prefix: prefix}, kind, nil
}

// Hash считает keyed hash секрета. Pepper живёт в окружении сервиса, не в БД.
func Hash(pepper []byte, secret string) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(secret))
	return mac.Sum(nil)
}

// Equal сравнивает хеши за постоянное время.
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
