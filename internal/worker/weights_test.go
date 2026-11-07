package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncWeightsDownloadsAndDeletesOnlyTheAssignment(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-layer.gguf")
	if err := os.WriteFile(old, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := syncWeights(context.Background(), dir, []WeightFile{{
		Name: "layer-a.gguf",
		URL:  "https://example.test/layer-a",
	}}, []string{"old-layer.gguf", "../outside", "layer-a.gguf"}, func(_ context.Context, file WeightFile, dest string) error {
		if file.Name != "layer-a.gguf" {
			t.Fatalf("скачивается %s", file.Name)
		}
		return os.WriteFile(dest, []byte("weights"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "layer-a.gguf")); err != nil {
		t.Fatalf("новый кусок не появился: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("старый кусок остался: %v", err)
	}
	if raw, err := os.ReadFile(other); err != nil || string(raw) != "keep" {
		t.Fatalf("посторонний файл изменён: %v %q", err, raw)
	}
}

func TestSyncWeightsSkipsFileWithMatchingHash(t *testing.T) {
	dir := t.TempDir()
	body := []byte("already")
	file, err := os.Open(writeTemp(t, dir, "layer-a.gguf", body))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := HashBytes(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = syncWeights(context.Background(), dir, []WeightFile{{
		Name:   "layer-a.gguf",
		SHA256: sum,
		URL:    "https://example.test/layer-a",
	}}, nil, func(context.Context, WeightFile, string) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("файл с верным хешем скачивался заново")
	}
}

func TestTreeBytesCountsNestedFiles(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "a.bin", []byte("12345"))
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, sub, "b.bin", []byte("ab"))
	if got := treeBytes(dir); got != 7 {
		t.Fatalf("размер = %d, ожидалось 7", got)
	}
	if got := treeBytes(""); got != 0 {
		t.Fatalf("пустой каталог дал %d", got)
	}
}

func TestHeldPieceNames(t *testing.T) {
	dir := t.TempDir()
	rev := filepath.Join(dir, "rev")
	if err := os.MkdirAll(rev, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, rev, "a.gguf", []byte("a"))
	writeTemp(t, rev, "a.gguf.part", []byte("partial"))
	names := HeldPieceNames(dir)
	if len(names) != 1 || names[0] != "a.gguf" {
		t.Fatalf("имена = %v", names)
	}
}

func writeTemp(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
