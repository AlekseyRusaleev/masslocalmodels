package workercfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstWritableDirSkipsClosedDirectory(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(root, "open")
	got := firstWritableDir([]string{filepath.Join(blocked, "models"), open})
	if got != open {
		t.Fatalf("каталог = %s, ожидался %s", got, open)
	}
	if _, err := os.Stat(open); err != nil {
		t.Fatal(err)
	}
}
