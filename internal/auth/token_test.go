package auth

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(token.KeyID) != 12 {
		t.Fatalf("key id length = %d, want 12", len(token.KeyID))
	}
	if len(token.Secret) != 43 {
		t.Fatalf("secret length = %d, want 43", len(token.Secret))
	}

	parsed, err := Parse(token.String())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.KeyID != token.KeyID || parsed.Secret != token.Secret {
		t.Fatalf("parsed token mismatch")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"no prefix":    "abc.def",
		"no dot":       TokenPrefix + "abc",
		"empty key id": TokenPrefix + ".secret",
		"empty secret": TokenPrefix + "keyid.",
		"too long":     TokenPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa." + "b",
	}
	for name, raw := range cases {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// Сессия панели и ключ программы различаются префиксом, но разбираются одной
// функцией: обработчику нужно знать, в какой таблице проверять секрет.
func TestParseAnyDistinguishesKinds(t *testing.T) {
	key, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	parsed, kind, err := ParseAny(key.String())
	if err != nil || kind != KindAPIKey {
		t.Fatalf("ключ разобран как %v, ошибка %v", kind, err)
	}
	if parsed.String() != key.String() {
		t.Fatalf("ключ потерял префикс: %q", parsed.String())
	}

	session, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if !strings.HasPrefix(session.String(), SessionPrefix) {
		t.Fatalf("у сессии не тот префикс: %q", session.String())
	}
	parsedSession, kind, err := ParseAny(session.String())
	if err != nil || kind != KindSession {
		t.Fatalf("сессия разобрана как %v, ошибка %v", kind, err)
	}
	if parsedSession.KeyID != session.KeyID || parsedSession.Secret != session.Secret {
		t.Fatal("сессия разобрана неверно")
	}

	// Parse принимает только ключи: сессию он обязан отвергнуть, иначе проверка
	// ключа могла бы пройти по чужой таблице.
	if _, err := Parse(session.String()); err == nil {
		t.Fatal("Parse принял токен сессии")
	}
}

func TestHashIsKeyed(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	a := Hash([]byte("pepper-one"), token.Secret)
	b := Hash([]byte("pepper-two"), token.Secret)
	if Equal(a, b) {
		t.Fatal("hash must depend on pepper")
	}

	c := Hash([]byte("pepper-one"), token.Secret)
	if !Equal(a, c) {
		t.Fatal("hash must be deterministic for same pepper")
	}
	if Equal(a, c[:len(c)-1]) {
		t.Fatal("different lengths must not compare equal")
	}
}
