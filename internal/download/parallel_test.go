package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rangeServer отдаёт payload с поддержкой Range.
func rangeServer(t *testing.T, payload []byte, supportRange bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if !supportRange || rangeHeader == "" {
			w.Write(payload)
			return
		}
		spec := strings.TrimPrefix(rangeHeader, "bytes=")
		startRaw, endRaw, ok := strings.Cut(spec, "-")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		start, err := strconv.Atoi(startRaw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		end := len(payload) - 1
		if endRaw != "" {
			if parsed, err := strconv.Atoi(endRaw); err == nil {
				end = parsed
			}
		}
		if start > end || end >= len(payload) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[start : end+1])
	}))
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestParallelDownloadAssemblesInOrder(t *testing.T) {
	// 2 МБ данных и низкий порог, чтобы включился многопоточный путь.
	payload := make([]byte, 2<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	old := parallelThreshold
	parallelThreshold = 1024
	defer func() { parallelThreshold = old }()

	server := rangeServer(t, payload, true)
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "big.bin")
	result, err := Fetch(context.Background(), Options{
		URL:         server.URL,
		DestPath:    dest,
		ExpectedSHA: hashOf(payload),
		Retries:     1,
		Streams:     4,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Bytes != int64(len(payload)) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len(payload))
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("assembled file differs from source")
	}

	// части должны быть удалены
	matches, _ := filepath.Glob(dest + ".part.chunk-*")
	if len(matches) != 0 {
		t.Fatalf("chunk files left behind: %v", matches)
	}
}

func TestFallsBackWhenServerIgnoresRange(t *testing.T) {
	payload := make([]byte, 2<<20)
	for i := range payload {
		payload[i] = byte((i * 7) % 253)
	}

	old := parallelThreshold
	parallelThreshold = 1024
	defer func() { parallelThreshold = old }()

	server := rangeServer(t, payload, false)
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "fallback.bin")
	result, err := Fetch(context.Background(), Options{
		URL:         server.URL,
		DestPath:    dest,
		ExpectedSHA: hashOf(payload),
		Retries:     1,
		Streams:     4,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Resumed {
		t.Fatal("should not resume")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("fallback file differs from source")
	}
}
