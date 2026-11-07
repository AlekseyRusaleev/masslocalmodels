package httpapi

import (
	"testing"
	"time"
)

// Корзина токенов: всплеск разрешён, дальше — отказ с временем ожидания.
func TestRateLimiterBurstThenRefuse(t *testing.T) {
	limiter := newRateLimiter(60, 3) // 60 в минуту, всплеск 3

	for attempt := 0; attempt < 3; attempt++ {
		if ok, _ := limiter.allow("user:a"); !ok {
			t.Fatalf("отказ на попытке %d в пределах всплеска", attempt)
		}
	}

	ok, retry := limiter.allow("user:a")
	if ok {
		t.Fatal("после исчерпания всплеска запрос разрешён")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("непонятное время ожидания: %v", retry)
	}

	// Другой аккаунт не страдает от чужого всплеска.
	if ok, _ := limiter.allow("user:b"); !ok {
		t.Fatal("лимит одного аккаунта распространился на другой")
	}
}

// Лимит пополняется со временем: после паузы запрос снова проходит.
func TestRateLimiterRefills(t *testing.T) {
	limiter := newRateLimiter(600, 1) // 10 в секунду
	if ok, _ := limiter.allow("user:a"); !ok {
		t.Fatal("первый запрос отклонён")
	}
	if ok, _ := limiter.allow("user:a"); ok {
		t.Fatal("второй запрос прошёл без паузы")
	}

	// Ждём чуть больше, чем нужно на один токен (100 мс при 10/с).
	time.Sleep(150 * time.Millisecond)
	if ok, _ := limiter.allow("user:a"); !ok {
		t.Fatal("после паузы лимит не пополнился")
	}
}

// Нулевое ограничение означает «без лимита».
func TestRateLimiterDisabled(t *testing.T) {
	limiter := newRateLimiter(0, 0)
	if limiter != nil {
		t.Fatal("нулевой лимит должен отключать ограничитель")
	}
	for attempt := 0; attempt < 100; attempt++ {
		if ok, _ := limiter.allow("user:a"); !ok {
			t.Fatal("отключённый лимит отказал")
		}
	}
}
