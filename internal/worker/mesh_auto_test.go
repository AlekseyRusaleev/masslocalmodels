package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/private-compute/network/internal/workercfg"
)

func TestLlamaDirCompleteRequiresBothBinaries(t *testing.T) {
	dir := t.TempDir()
	if llamaDirComplete(dir) {
		t.Fatal("пустой каталог не должен считаться готовым")
	}
	if err := os.WriteFile(filepath.Join(dir, executableName("llama-server")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if llamaDirComplete(dir) {
		t.Fatal("одного llama-server недостаточно")
	}
	if err := os.WriteFile(filepath.Join(dir, executableName("ggml-rpc-server")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !llamaDirComplete(dir) {
		t.Fatal("оба файла на месте, каталог должен подойти")
	}
}

func TestPrepareAutomaticMeshKeepsAManualGroup(t *testing.T) {
	cfg := workercfg.Config{
		Model: "gpt-oss:20b",
		Mesh:  &workercfg.Mesh{Role: meshRoleCoordinator, Model: "gpt-oss:20b"},
	}
	got := prepareAutomaticMesh(cfg)
	if got.Mesh.Role != meshRoleCoordinator {
		t.Fatalf("роль сменилась на %s", got.Mesh.Role)
	}
}

func TestPrepareAutomaticMeshStaysOffWithoutWeights(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"llama-server", "ggml-rpc-server"} {
		if err := os.WriteFile(filepath.Join(dir, executableName(name)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COMPUTE_LLAMA_DIR", dir)
	weights := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(weights, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := prepareAutomaticMesh(workercfg.Config{Model: "missing-model"})
	if cfg.Mesh != nil {
		t.Fatal("без файла весов шард включаться не должен")
	}
}
