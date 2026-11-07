package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/private-compute/network/internal/workercfg"
)

func TestDraftSpecUsesTheHeadWhenTheFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "head.gguf")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := draftSpecArgs(path, "")
	if err != nil {
		t.Fatal(err)
	}
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

func TestDraftSpecUsesNgramWhenNoHeadWasRequested(t *testing.T) {
	got, err := draftSpecArgs("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "--spec-type" || got[1] != SpecNgram {
		t.Fatalf("флаги %v", got)
	}
}

func TestDraftSpecNoneStaysOffEvenWhenTheHeadExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "head.gguf")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := draftSpecArgs(path, SpecNone)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != SpecNone {
		t.Fatalf("выключение подменилось: %v", got)
	}
	ngram, err := draftSpecArgs(path, SpecNgram)
	if err != nil {
		t.Fatal(err)
	}
	if len(ngram) != 2 || ngram[1] != SpecNgram {
		t.Fatalf("ngram прикрепил голову: %v", ngram)
	}
}

func TestDraftSpecDoesNotReplaceAMissingHeadWithNgram(t *testing.T) {
	_, err := draftSpecArgs(filepath.Join(t.TempDir(), "no-such-head.gguf"), DefaultDraftSpec)
	if err == nil {
		t.Fatal("пропавший файл головы молча стал ngram")
	}
}

func TestMeshConfigKeyTracksSpecMode(t *testing.T) {
	base := workercfg.Mesh{Role: meshRoleCoordinator, Model: "gpt-oss:20b", Ctx: 4096}
	other := base
	other.SpecType = SpecNone
	if meshConfigKey(base) == meshConfigKey(other) {
		t.Fatal("ключ не изменился при выключении черновика")
	}
	other = base
	other.DraftGGUFPath = filepath.Join(t.TempDir(), "head.gguf")
	if meshConfigKey(base) == meshConfigKey(other) {
		t.Fatal("ключ не изменился при смене файла головы")
	}
}
