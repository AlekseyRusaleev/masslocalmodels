package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newSilentLogger собирает журнал, который не мешает выводу тестов.
func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Аренда задания подтверждается, пока узел готовит ответ: группа слоёв
// грузится минутами, а без продления задание возвращалось в очередь через
// 90 секунд и падало после третьей попытки.
func TestLeaseRenewalKeepsJobAlive(t *testing.T) {
	old := leaseRenewEvery
	leaseRenewEvery = 10 * time.Millisecond
	defer func() { leaseRenewEvery = old }()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/worker/v1/jobs/job-1/lease" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token-1")
	stop := startLeaseRenewal(context.Background(), client, "job-1", newSilentLogger())

	time.Sleep(80 * time.Millisecond)
	stop()

	got := hits.Load()
	if got < 3 {
		t.Fatalf("подтверждений аренды %d, ожидалось не меньше 3", got)
	}

	// После остановки запросов быть не должно: иначе узел продолжит
	// подтверждать задание, которое уже завершено.
	time.Sleep(40 * time.Millisecond)
	if after := hits.Load(); after != got {
		t.Fatalf("после остановки пришло ещё %d запросов", after-got)
	}
}

// Ошибка платформы не должна ронять узел: аренду подтверждают повторно.
func TestLeaseRenewalSurvivesServerError(t *testing.T) {
	old := leaseRenewEvery
	leaseRenewEvery = 10 * time.Millisecond
	defer func() { leaseRenewEvery = old }()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "token-1")
	stop := startLeaseRenewal(context.Background(), client, "job-1", newSilentLogger())
	time.Sleep(60 * time.Millisecond)
	stop()

	if got := hits.Load(); got < 2 {
		t.Fatalf("после ошибки сервера повторов не было: %d запросов", got)
	}
}
