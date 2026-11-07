package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Ограничение частоты запросов к инференсу.
//
// Зачем: без него один клиент с ключом занимает все узлы сети — очередь общая,
// и честная доля спасает только тех, кто уже в очереди. Лимит стоит до
// создания задания, поэтому лишние запросы не доходят ни до узлов, ни до
// журнала заданий.
const (
	// defaultRatePerMinute — сколько запросов в минуту разрешено аккаунту.
	defaultRatePerMinute = 60
	// defaultRateBurst — сколько запросов можно отправить без паузы.
	defaultRateBurst = 10
)

// rateLimiter — корзина токенов на каждый аккаунт.
type rateLimiter struct {
	mu        sync.Mutex
	perMinute float64
	burst     float64
	buckets   map[string]*rateBucket
}

type rateBucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter создаёт ограничитель частоты для запуска сервиса.
func NewRateLimiter(perMinute, burst int) *rateLimiter { return newRateLimiter(perMinute, burst) }

// newRateLimiter создаёт ограничитель. Ноль или отрицательное значение
// отключают ограничение: локальные стенды не должны упираться в лимит.
func newRateLimiter(perMinute, burst int) *rateLimiter {
	if perMinute <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = defaultRateBurst
	}
	return &rateLimiter{
		perMinute: float64(perMinute),
		burst:     float64(burst),
		buckets:   make(map[string]*rateBucket, 32),
	}
}

// allow списывает один запрос и сообщает, когда повторить, если нельзя.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}

	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &rateBucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = bucket
	}

	// Пополнение: скорость perMinute, дробные токены сохраняются — иначе при
	// частых коротких запросах лимит никогда бы не пополнялся.
	if elapsed := now.Sub(bucket.lastSeen).Minutes(); elapsed > 0 {
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.perMinute)
		bucket.lastSeen = now
	}

	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}

	deficit := 1 - bucket.tokens
	wait := time.Duration(deficit / l.perMinute * float64(time.Minute))
	if wait < time.Second {
		wait = time.Second
	}

	// Уборка старых записей: ключей может быть много, память не бесконечна.
	if len(l.buckets) > 4096 {
		for existing, entry := range l.buckets {
			if now.Sub(entry.lastSeen) > 10*time.Minute {
				delete(l.buckets, existing)
			}
		}
	}
	return false, wait
}

// rateLimited проверяет лимит аккаунта и сам пишет ответ 429.
//
// Возвращает true, если запрос можно выполнять. Стоит внутри requireAuth,
// потому что лимит считается по аккаунту, а не по адресу.
func (cfg Config) rateLimited(w http.ResponseWriter, p principal) bool {
	key := p.UserID
	if key == "" {
		key = "anonymous"
	}
	ok, retry := cfg.RateLimiter.allow(key)
	if ok {
		return true
	}

	seconds := int(math.Ceil(retry.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, http.StatusTooManyRequests, "rate_limited",
		"too many requests; retry in "+strconv.Itoa(seconds)+"s")
	return false
}
