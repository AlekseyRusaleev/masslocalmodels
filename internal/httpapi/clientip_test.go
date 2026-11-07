package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	fromLoopback := httptest.NewRequest("POST", "/v1/nodes/register", nil)
	fromLoopback.RemoteAddr = "127.0.0.1:54321"
	fromLoopback.Header.Set("X-Real-IP", "203.0.113.10")
	if got := clientIP(fromLoopback); got != "203.0.113.10" {
		t.Fatalf("за nginx: %s", got)
	}

	spoofed := httptest.NewRequest("POST", "/v1/nodes/register", nil)
	spoofed.RemoteAddr = "198.51.100.4:1234"
	spoofed.Header.Set("X-Real-IP", "203.0.113.10")
	if got := clientIP(spoofed); got != "198.51.100.4" {
		t.Fatalf("прямой адрес нельзя подменить: %s", got)
	}

	bare := httptest.NewRequest("POST", "/v1/nodes/register", nil)
	bare.RemoteAddr = "127.0.0.1:9"
	if got := clientIP(bare); got != "127.0.0.1" {
		t.Fatalf("без заголовка: %s", got)
	}
}

func TestJoinCodeMatches(t *testing.T) {
	if joinCodeMatches("", "anything") {
		t.Fatal("пустой код не должен открывать самозапись")
	}
	if !joinCodeMatches("network-secret", "network-secret") {
		t.Fatal("верный код отклонён")
	}
	if joinCodeMatches("network-secret", "network-secreT") {
		t.Fatal("чужой код принят")
	}
	if joinCodeMatches("short", "longer") {
		t.Fatal("разная длина не должна совпадать")
	}
}
