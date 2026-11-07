package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Механика туннеля: два односторонних потока, байты в обе стороны.
//
// Тест намеренно не использует llama.cpp и БД: ошибка в перекачке выглядит
// там как зависание загрузки модели, и найти её без такого теста нельзя.
func TestMeshStreamRelayBothWays(t *testing.T) {
	stream := newMeshStream()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// «Вниз» отдаёт то, что пришло «вверх».
	var received bytes.Buffer
	var mu sync.Mutex
	downDone := make(chan struct{})
	go func() {
		defer close(downDone)
		stream.readTo(ctx, writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return received.Write(p)
		}), nil)
	}()

	// «Вверх» пишет двумя кусками, как это делает RPC.
	if err := stream.write([]byte("first-")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := stream.write([]byte("second")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := received.String()
		mu.Unlock()
		if got == "first-second" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	got := received.String()
	mu.Unlock()
	if got != "first-second" {
		t.Fatalf("получено %q, ожидалось %q", got, "first-second")
	}

	// Закрытие «вверх» заканчивает «вниз».
	stream.close()
	select {
	case <-downDone:
	case <-time.After(3 * time.Second):
		t.Fatal("поток «вниз» не завершился после закрытия")
	}
}

// Переполнение буфера обязано закрывать поток, а не расти без предела.
func TestMeshStreamOverflow(t *testing.T) {
	stream := newMeshStream()
	chunk := make([]byte, 1<<20)
	for i := 0; i < meshStreamLimit/(1<<20); i++ {
		if err := stream.write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := stream.write(chunk); err == nil {
		t.Fatal("переполнение должно возвращать ошибку")
	}
}

// writerFunc превращает функцию в io.Writer (в тесте свой, чтобы не зависеть
// от внутренних деталей обработчика).
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Сквозная проверка обработчика «вниз»: он должен отдавать данные, пришедшие
// в буфер, и закрываться, когда поток закрыт.
func TestPumpDownStreamsBuffer(t *testing.T) {
	stream := newMeshStream()
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = stream.write([]byte("payload"))
		time.Sleep(50 * time.Millisecond)
		stream.close()
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		stream.readTo(r.Context(), w, func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		})
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q, want payload", string(body))
	}
}
