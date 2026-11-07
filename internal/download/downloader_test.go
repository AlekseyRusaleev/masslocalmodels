package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFetchVerifiesChecksum(t *testing.T) {
	payload := []byte("model shard bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "shard.bin")
	result, err := Fetch(context.Background(), Options{
		URL:         server.URL,
		DestPath:    dest,
		ExpectedSHA: shaOf(payload),
		Retries:     1,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Bytes != int64(len(payload)) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len(payload))
	}
	if result.SHA256 != shaOf(payload) {
		t.Fatalf("sha mismatch in result")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("destination missing: %v", err)
	}
}

func TestFetchRejectsWrongChecksum(t *testing.T) {
	payload := []byte("tampered")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "shard.bin")
	_, err := Fetch(context.Background(), Options{
		URL:         server.URL,
		DestPath:    dest,
		ExpectedSHA: shaOf([]byte("original")),
		Retries:     1,
	})
	if err == nil {
		t.Fatal("expected checksum error")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("destination must not exist after checksum failure")
	}
}

func TestFetchResumesFromPartialFile(t *testing.T) {
	payload := []byte("0123456789abcdef")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Write(payload)
			return
		}
		start, err := strconv.Atoi(rangeHeader[len("bytes=") : len(rangeHeader)-1])
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[start:])
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "resume.bin")
	part := dest + ".part"
	if err := os.WriteFile(part, payload[:6], 0o644); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	result, err := Fetch(context.Background(), Options{
		URL:         server.URL,
		DestPath:    dest,
		ExpectedSHA: shaOf(payload),
		Retries:     1,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !result.Resumed {
		t.Fatal("expected resumed download")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("content = %q, want %q", data, payload)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1024": 1024,
		"5G":   5 << 30,
		"500M": 500 << 20,
		"16K":  16 << 10,
	}
	for raw, want := range cases {
		got, err := ParseSize(raw)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseSize(%q) = %d, want %d", raw, got, want)
		}
	}
	if _, err := ParseSize("abc"); err == nil {
		t.Fatal("expected error for invalid size")
	}
}
