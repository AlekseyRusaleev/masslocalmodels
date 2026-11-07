package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDraftSpecUsesTheHeadWhenTheFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "head.gguf")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := draftSpecArgs(path, "")
	want := []string{"-md", path, "--spec-type", DefaultDraftSpec}
	if len(got) != len(want) {
		t.Fatalf("флаги %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("флаг %d = %s, нужно %s", i, got[i], want[i])
		}
	}
}

func TestDraftSpecFallsBackToNgram(t *testing.T) {
	got := draftSpecArgs("", "")
	if len(got) != 2 || got[0] != "--spec-type" || got[1] != SpecNgram {
		t.Fatalf("флаги %v", got)
	}
	missing := draftSpecArgs(filepath.Join(t.TempDir(), "no-such-head.gguf"), DefaultDraftSpec)
	if len(missing) != 2 || missing[1] != SpecNgram {
		t.Fatalf("пропавший файл головы не должен отключать черновик: %v", missing)
	}
}
