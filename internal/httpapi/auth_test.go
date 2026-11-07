package httpapi

import (
	"strings"
	"testing"
)

// Администратор панели должен получать все права, участник — только чат и чтение.
func TestScopesForRole(t *testing.T) {
	admin := scopesForRole("admin")
	if !hasScope(admin, "admin") || !hasScope(admin, "chat:write") {
		t.Fatalf("администратор без прав: %v", admin)
	}
	user := scopesForRole("user")
	if hasScope(user, "admin") {
		t.Fatalf("участник получил административные права: %v", user)
	}
	if !hasScope(user, "chat:write") || !hasScope(user, "models:read") {
		t.Fatalf("участнику не хватает прав на чат: %v", user)
	}
}

// Короткий пароль и пустой пароль отклоняются, нормальный проходит.
func TestPasswordProblem(t *testing.T) {
	if problem := passwordProblem("short"); problem == "" {
		t.Fatal("короткий пароль принят")
	}
	if problem := passwordProblem("          "); problem == "" {
		t.Fatal("пароль из пробелов принят")
	}
	if problem := passwordProblem("correct-horse-battery"); problem != "" {
		t.Fatalf("нормальный пароль отклонён: %s", problem)
	}
}

// Хеш bcrypt не совпадает с паролем и проверяется обратно.
func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("хеш не посчитан: %v", err)
	}
	if strings.Contains(hash, "correct-horse-battery") {
		t.Fatal("пароль попал в хеш открытым текстом")
	}
	if err := comparePassword(hash, "correct-horse-battery"); err != nil {
		t.Fatalf("верный пароль не принят: %v", err)
	}
	if err := comparePassword(hash, "wrong-password"); err == nil {
		t.Fatal("неверный пароль принят")
	}
}

// Имя сессии угадывается по строке браузера, незнакомая строка не теряется.
func TestSessionName(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Windows NT 10.0) ... Edg/120.0":      "Edge",
		"Mozilla/5.0 (X11; Linux) Chrome/120.0 Safari/...": "Chrome",
		"curl/8.4.0": "curl",
		"":           "unknown client",
		"custom/1.0": "custom/1.0",
	}
	for agent, want := range cases {
		if got := sessionName(agent); got != want {
			t.Fatalf("sessionName(%q) = %q, ожидалось %q", agent, got, want)
		}
	}
}

// Ограничитель перебора: после десяти неудач ключ блокируется.
func TestLoginLimiterBlocksAfterFailures(t *testing.T) {
	limiter := newLoginLimiter()
	key := "127.0.0.1|user@example.test"
	for attempt := 0; attempt < loginAttempts; attempt++ {
		if limiter.blocked(key) {
			t.Fatalf("заблокирован на попытке %d", attempt)
		}
		limiter.fail(key)
	}
	if !limiter.blocked(key) {
		t.Fatal("после лимита неудач вход не заблокирован")
	}
	limiter.reset(key)
	if limiter.blocked(key) {
		t.Fatal("успешный вход не снял блокировку")
	}
	// Другой адрес не страдает от чужого перебора.
	if limiter.blocked("10.0.0.1|user@example.test") {
		t.Fatal("блокировка распространилась на другой адрес")
	}
}

// truncate не разрывает UTF-8 посередине руны.
func TestTruncateKeepsValidUTF8(t *testing.T) {
	text := strings.Repeat("узел", 20)
	got := truncate(text, 5)
	if !strings.HasPrefix(text, got) {
		t.Fatalf("обрезанная строка не является префиксом: %q", got)
	}
	if len([]rune(got)) != 5 {
		t.Fatalf("ожидалось 5 рун, получено %d", len([]rune(got)))
	}
}
