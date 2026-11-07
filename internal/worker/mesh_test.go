package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/private-compute/network/internal/store"
	"github.com/private-compute/network/internal/workercfg"
)

// Роль по умолчанию — координатор: узел, которому не указали роль, должен
// обслуживать задания, а не молча стоять в режиме стадии.
func TestMeshNormalizeDefaults(t *testing.T) {
	got := MeshNormalize(workercfg.Mesh{})
	if got.Role != meshRoleCoordinator {
		t.Fatalf("role = %q, want coordinator", got.Role)
	}
	if got.ServerPort != DefaultMeshServerPort {
		t.Fatalf("server port = %d, want %d", got.ServerPort, DefaultMeshServerPort)
	}
	if got.Ctx != DefaultNumCtx {
		t.Fatalf("ctx = %d, want %d", got.Ctx, DefaultNumCtx)
	}

	compute := MeshNormalize(workercfg.Mesh{Role: meshRoleCompute})
	if compute.RPCPort != DefaultMeshRPCPort {
		t.Fatalf("rpc port = %d, want %d", compute.RPCPort, DefaultMeshRPCPort)
	}
	// RPC-порт не имеет ни TLS, ни авторизации: по умолчанию только loopback.
	if compute.ListenHost != "127.0.0.1" {
		t.Fatalf("listen host = %q, want 127.0.0.1", compute.ListenHost)
	}
}

// Платформа отправляет задания по полю served_model, поэтому координатор
// обязан объявлять имя шард-модели, а стадия — ничего не объявлять.
func TestMeshServedModel(t *testing.T) {
	plain := workercfg.Config{Model: "qwen3:8b"}
	if got := MeshServedModel(plain); got != "" {
		t.Fatalf("без mesh = %q, want пусто", got)
	}

	coordinator := workercfg.Config{
		Model: "qwen3:8b",
		Mesh:  &workercfg.Mesh{Role: meshRoleCoordinator, Model: "qwen3:32b"},
	}
	if got := MeshServedModel(coordinator); got != "qwen3:32b" {
		t.Fatalf("координатор = %q, want qwen3:32b", got)
	}

	// Без явного имени координатор обслуживает модель из конфигурации.
	implicit := workercfg.Config{
		Model: "qwen3:8b",
		Mesh:  &workercfg.Mesh{Role: meshRoleCoordinator},
	}
	if got := MeshServedModel(implicit); got != "qwen3:8b" {
		t.Fatalf("координатор без имени = %q, want qwen3:8b", got)
	}

	compute := workercfg.Config{
		Model: "qwen3:8b",
		Mesh:  &workercfg.Mesh{Role: meshRoleCompute, Model: "qwen3:32b"},
	}
	if got := MeshServedModel(compute); got != "" {
		t.Fatalf("стадия = %q, want пусто: она не обслуживает API", got)
	}
}

// Смена соисполнителей или окна контекста обязана перезапускать группу:
// иначе узел продолжит работать со старой раскладкой слоёв.
func TestMeshConfigKeyTracksPeers(t *testing.T) {
	base := workercfg.Mesh{Role: meshRoleCoordinator, Model: "qwen3:8b", Ctx: 8192}
	other := base
	other.Peers = []string{"10.0.0.2:50052"}
	if meshConfigKey(base) == meshConfigKey(other) {
		t.Fatal("ключ не изменился при смене соисполнителей")
	}

	wider := base
	wider.Ctx = 16384
	if meshConfigKey(base) == meshConfigKey(wider) {
		t.Fatal("ключ не изменился при смене окна контекста")
	}
}

// Топология задаёт роль и соисполнителей: координатор — тот, кого назначили,
// а стадии без объявленного адреса обязаны попасть в Missing, иначе группа
// поднимется и молча потеряет часть слоёв.
func TestResolveMeshFromTopology(t *testing.T) {
	coordinator := "w1"
	detail := store.TopologyDetail{
		Topology: store.Topology{
			ID: "t1", Version: 3, ModelName: "qwen3:8b",
			CoordinatorID: &coordinator, Status: "active",
		},
		Stages: []store.Stage{
			{WorkerID: "w1", WorkerName: "owner-pc", LayerStart: 0, LayerEnd: 17, RPCAddr: "127.0.0.1:50053"},
			{WorkerID: "w2", WorkerName: "cpu-stage", LayerStart: 18, LayerEnd: 35, RPCAddr: "10.0.0.5:50052"},
		},
	}

	plan, err := ResolveMeshFromTopology(workercfg.Mesh{}, "w1", detail)
	if err != nil {
		t.Fatalf("coordinator: %v", err)
	}
	if plan.Role != meshRoleCoordinator {
		t.Fatalf("role = %q, want coordinator", plan.Role)
	}
	if plan.Layers != "0-17" {
		t.Fatalf("layers = %q, want 0-17", plan.Layers)
	}
	if len(plan.Peers) != 1 || plan.Peers[0].RPCAddr != "10.0.0.5:50052" {
		t.Fatalf("peers = %v, want адрес 10.0.0.5:50052", plan.Peers)
	}
	if plan.Peers[0].WorkerID != "w2" {
		t.Fatalf("peer worker = %q, want w2", plan.Peers[0].WorkerID)
	}
	if plan.Source != "topology" || plan.Version != 3 {
		t.Fatalf("source/version = %q/%d", plan.Source, plan.Version)
	}

	stagePlan, err := ResolveMeshFromTopology(workercfg.Mesh{}, "w2", detail)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if stagePlan.Role != meshRoleCompute {
		t.Fatalf("role = %q, want compute", stagePlan.Role)
	}
	if len(stagePlan.Peers) != 0 {
		t.Fatalf("у стадии не должно быть соисполнителей: %v", stagePlan.Peers)
	}

	// Стадия без адреса: группа не должна считаться готовой.
	broken := detail
	broken.Stages = []store.Stage{
		{WorkerID: "w1", WorkerName: "owner-pc", LayerStart: 0, LayerEnd: 17},
		{WorkerID: "w2", WorkerName: "cpu-stage", LayerStart: 18, LayerEnd: 35},
	}
	plan, err = ResolveMeshFromTopology(workercfg.Mesh{}, "w1", broken)
	if err != nil {
		t.Fatalf("missing addr: %v", err)
	}
	if len(plan.Missing) != 1 || plan.Missing[0] != "cpu-stage" {
		t.Fatalf("missing = %v, want [cpu-stage]", plan.Missing)
	}

	// Узел вне топологии получает ошибку, а не роль наугад.
	if _, err := ResolveMeshFromTopology(workercfg.Mesh{}, "w9", detail); err == nil {
		t.Fatal("узел вне топологии должен давать ошибку")
	}
}

// Оценка памяти: половина весов плюс два гигабайта, но не меньше трёх.
// Порог между двумя наблюдёнными точками (1.1 ГБ — падение, 6 ГБ — успех).
func TestMeshMemoryNeed(t *testing.T) {
	fiveGB := uint64(5) << 30
	if got, want := meshMemoryNeed(fiveGB), uint64(4.5*(1<<30)); got != want {
		t.Fatalf("5 GiB weights: got %d, want %d", got, want)
	}
	if got, want := meshMemoryNeed(1<<20), uint64(3)<<30; got != want {
		t.Fatalf("tiny weights: got %d, want %d", got, want)
	}
}

// План памяти по реальному файлу: размер весов и порог берутся из него.
func TestPlanMeshMemorySmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	plan, err := PlanMeshMemory(context.Background(), path)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.WeightsBytes != 1024 {
		t.Fatalf("weights = %d, want 1024", plan.WeightsBytes)
	}
	if plan.NeededBytes != uint64(3)<<30 {
		t.Fatalf("needed = %d, want 3 GiB", plan.NeededBytes)
	}
}

// С включённым туннелем адрес стадии не нужен: стороны сходятся по
// идентификаторам узлов, и стадия не попадает в Missing.
func TestResolveMeshFromTopologyTunnel(t *testing.T) {
	coordinator := "w1"
	detail := store.TopologyDetail{
		Topology: store.Topology{
			ID: "t2", Version: 1, ModelName: "qwen3:8b",
			CoordinatorID: &coordinator, Status: "active",
		},
		Stages: []store.Stage{
			{WorkerID: "w1", WorkerName: "owner-pc", LayerStart: 0, LayerEnd: 17},
			{WorkerID: "w2", WorkerName: "far-stage", LayerStart: 18, LayerEnd: 35},
		},
	}

	plan, err := ResolveMeshFromTopology(workercfg.Mesh{Tunnel: true}, "w1", detail)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(plan.Missing) != 0 {
		t.Fatalf("с туннелем адреса не обязательны, а Missing = %v", plan.Missing)
	}
	if len(plan.Peers) != 1 || plan.Peers[0].WorkerID != "w2" {
		t.Fatalf("peers = %v, want одного соисполнителя w2", plan.Peers)
	}
	if plan.Peers[0].RPCAddr != "" {
		t.Fatalf("адрес не должен выдумываться: %q", plan.Peers[0].RPCAddr)
	}
}

// Меш-лог не должен уходить в файл с расширением .log в рабочем каталоге
// llama.cpp: там уже лежат посторонние файлы.
func TestMeshLogPathIsNamed(t *testing.T) {
	path := meshLogPath(meshRoleCoordinator)
	if path == "" {
		t.Skip("каталог журналов недоступен")
	}
	if !strings.HasSuffix(path, "compute-mesh-coordinator.log") {
		t.Fatalf("path = %q, want суффикс compute-mesh-coordinator.log", path)
	}
}
