package worker

// Шардирование модели между узлами через llama.cpp RPC (ADR 0008).
//
// Зачем отдельный рантайм: Ollama не умеет распределять слои между машинами.
// llama.cpp умеет это двумя частями — `ggml-rpc-server` отдаёт свои устройства
// (GPU или CPU) как дополнительный бэкенд, а `llama-server` на координаторе
// раскладывает слои модели по локальным и удалённым устройствам. Клиент при
// этом видит обычный OpenAI-совместимый API.
//
// Что здесь важно и не является деталью реализации:
//
//  1. Веса не передаются по сети в момент старта. Каждая стадия обязана иметь
//     локальный GGUF: измеренная скорость домашнего канала ~1.5 MB/s, и модель
//     на 5 ГБ через туннель грузилась больше 25 минут (ADR 0008 §5.2).
//     llama.cpp RPC всё равно пришлёт тензоры координатору — но один раз и с
//     локального кэша стадии, а не как единственный путь до весов.
//  2. Порт RPC не имеет ни TLS, ни авторизации. Поэтому ListenHost по
//     умолчанию 127.0.0.1, а наружу он выходит только через туннель.
//  3. Координатор объявляет платформе имя шард-модели и digest её весов —
//     тот же, что ждёт каталог, поэтому аттестация работает без поблажек.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/private-compute/network/internal/store"
	"github.com/private-compute/network/internal/workercfg"
)

const (
	// DefaultMeshServerPort — порт llama-server координатора.
	DefaultMeshServerPort = 18081
	// DefaultMeshRPCPort — порт ggml-rpc-server стадии.
	DefaultMeshRPCPort = 50052
	// meshReadyTimeout — сколько ждём готовности шард-группы. Холодный старт
	// включает передачу тензоров стадии и загрузку слоёв в VRAM.
	meshReadyTimeout = 5 * time.Minute
	// meshRemoteReadyTimeout — ожидание группы с удалённой стадией: тензоры
	// передаются по сети со скоростью канала (измерено 1.4 MB/s через VPS).
	meshRemoteReadyTimeout = 45 * time.Minute
	// meshRuntimeRoles — допустимые роли в шард-группе.
	meshRoleCoordinator = "coordinator"
	meshRoleCompute     = "compute"
	// meshRoleAuto — роль определяет топология на Control Plane: так узел не
	// требует ручной правки конфигурации при перекладывании слоёв.
	meshRoleAuto = "auto"
)

// MeshPeer — соисполнитель шард-группы.
//
// Адрес может отсутствовать: тогда связь идёт туннелем через платформу, и
// узлу достаточно знать идентификатор — адрес он получит сам.
type MeshPeer struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name,omitempty"`
	RPCAddr  string `json:"rpc_addr,omitempty"`
}

// ResolvedMesh — план участия узла в шард-группе.
//
// Собирается из активной топологии: роль, диапазон слоёв, соисполнители и имя
// модели приходят с платформы. Ручная конфигурация остаётся запасным путём.
type ResolvedMesh struct {
	Role          string     `json:"role"`
	Model         string     `json:"model,omitempty"`
	Layers        string     `json:"layers,omitempty"`
	Peers         []MeshPeer `json:"peers,omitempty"`
	Missing       []string   `json:"missing,omitempty"`
	TopologyID    string     `json:"topology_id,omitempty"`
	SelfWorkerID  string     `json:"self_worker_id,omitempty"`
	CoordinatorID string     `json:"coordinator_id,omitempty"`
	Version       int        `json:"version,omitempty"`
	Source        string     `json:"source"`
}

// ResolveMeshFromTopology переводит назначение платформы в план узла.
//
// Роль определяется полем coordinator_worker_id топологии, а не догадкой:
// задание обслуживает ровно тот узел, которого назначил администратор.
// Стадии без объявленного адреса попадают в Missing — это честнее, чем
// запустить группу, которая не сможет подключиться к части слоёв. Если
// включён туннель, адрес не нужен: стороны сходятся по идентификаторам.
func ResolveMeshFromTopology(cfg workercfg.Mesh, selfWorkerID string, detail store.TopologyDetail) (ResolvedMesh, error) {
	if len(detail.Stages) == 0 {
		return ResolvedMesh{}, fmt.Errorf("топология %s без стадий", detail.Topology.ID)
	}

	var self *store.Stage
	for index := range detail.Stages {
		if detail.Stages[index].WorkerID == selfWorkerID {
			self = &detail.Stages[index]
			break
		}
	}
	if self == nil {
		return ResolvedMesh{}, fmt.Errorf("узел %s не входит в топологию %s", selfWorkerID, detail.Topology.ID)
	}

	plan := ResolvedMesh{
		Model:        detail.Topology.ModelName,
		Layers:       fmt.Sprintf("%d-%d", self.LayerStart, self.LayerEnd),
		TopologyID:   detail.Topology.ID,
		SelfWorkerID: selfWorkerID,
		Version:      detail.Topology.Version,
		Source:       "topology",
	}
	if plan.Model == "" {
		plan.Model = cfg.Model
	}

	coordinator := detail.Topology.CoordinatorID != nil && *detail.Topology.CoordinatorID == selfWorkerID
	if !coordinator {
		plan.Role = meshRoleCompute
		if detail.Topology.CoordinatorID != nil {
			plan.CoordinatorID = *detail.Topology.CoordinatorID
		}
		return plan, nil
	}

	plan.Role = meshRoleCoordinator
	plan.CoordinatorID = selfWorkerID
	for _, stage := range detail.Stages {
		if stage.WorkerID == selfWorkerID {
			continue
		}
		name := stage.WorkerName
		if name == "" {
			name = stage.WorkerID
		}
		peer := MeshPeer{WorkerID: stage.WorkerID, Name: name, RPCAddr: strings.TrimSpace(stage.RPCAddr)}
		if peer.RPCAddr == "" && !cfg.Tunnel {
			plan.Missing = append(plan.Missing, name)
			continue
		}
		plan.Peers = append(plan.Peers, peer)
	}
	return plan, nil
}

// MeshPeerAddress собирает адрес RPC-стадии этого узла из его настроек.
func MeshPeerAddress(m workercfg.Mesh) string {
	m = MeshNormalize(m)
	if m.Role != meshRoleCompute {
		return ""
	}
	return net.JoinHostPort(m.ListenHost, fmt.Sprint(m.RPCPort))
}

// MeshNormalize заполняет умолчания роли.
func MeshNormalize(m workercfg.Mesh) workercfg.Mesh {
	if strings.TrimSpace(m.Role) == "" {
		m.Role = meshRoleCoordinator
	}
	if m.ListenHost == "" {
		m.ListenHost = "127.0.0.1"
	}
	if m.RPCPort == 0 {
		m.RPCPort = DefaultMeshRPCPort
	}
	if m.ServerPort == 0 {
		m.ServerPort = DefaultMeshServerPort
	}
	if m.Ctx == 0 {
		m.Ctx = DefaultNumCtx
	}
	if m.SpecType == "" && m.DraftGGUFPath != "" {
		m.SpecType = DefaultDraftSpec
	}
	return m
}

func withDraftFromConfig(cfg workercfg.Config, m workercfg.Mesh) workercfg.Mesh {
	if m.DraftGGUFPath == "" {
		m.DraftGGUFPath = cfg.DraftGGUFPath
	}
	return MeshNormalize(m)
}

// MeshServedModel — имя модели, которое узел объявляет платформе.
// Для координатора это имя шард-модели: по нему Scheduler отправляет задания
// именно на него, а не на любой узел с такой же локальной моделью.
func MeshServedModel(cfg workercfg.Config) string {
	if cfg.Mesh == nil {
		return ""
	}
	m := MeshNormalize(*cfg.Mesh)
	if m.Role != meshRoleCoordinator {
		return ""
	}
	if m.Model != "" {
		return m.Model
	}
	return cfg.Model
}

// MeshOptionsOf возвращает настройки шард-группы узла, если они заданы.
func MeshOptionsOf(cfg workercfg.Config) *workercfg.Mesh {
	if cfg.Mesh == nil {
		return nil
	}
	m := MeshNormalize(*cfg.Mesh)
	return &m
}

// meshPlan — последний план участия, полученный с платформы.
//
// Heartbeat-цикл обновляет его, цикл заданий читает. Топология может
// измениться в любой момент: администратор меняет раскладку слоёв, не трогая
// файлы на машинах.
var meshPlan struct {
	sync.RWMutex
	plan ResolvedMesh
	set  bool
}

// SetMeshPlan запоминает разрешённый план участия.
func SetMeshPlan(plan ResolvedMesh) {
	meshPlan.Lock()
	defer meshPlan.Unlock()
	meshPlan.plan = plan
	meshPlan.set = true
}

// CurrentMeshPlan возвращает последний план участия.
func CurrentMeshPlan() (ResolvedMesh, bool) {
	meshPlan.RLock()
	defer meshPlan.RUnlock()
	return meshPlan.plan, meshPlan.set
}

// RefreshMeshPlan спрашивает платформу, в какой шард-группе состоит узел.
//
// Возвращает план, если узел входит в активную топологию. Ошибка означает, что
// роль определить нельзя: тогда узел работает по локальной конфигурации.
func RefreshMeshPlan(ctx context.Context, client *Client, cfg workercfg.Config) (ResolvedMesh, error) {
	mesh := MeshOptionsOf(cfg)
	if mesh == nil {
		return ResolvedMesh{}, fmt.Errorf("шард-режим не настроен")
	}

	assignments, err := client.Assignments(ctx)
	if err != nil {
		return ResolvedMesh{}, err
	}

	// Модель из конфигурации важнее порядка: если узлов в сети несколько,
	// случайная топология чужой модели нам не подходит.
	for _, detail := range assignments.Assignments {
		if detail.Topology.Status != "active" && detail.Topology.Status != "staging" {
			continue
		}
		if cfg.Model != "" && detail.Topology.ModelName != "" && detail.Topology.ModelName != cfg.Model {
			continue
		}
		plan, err := ResolveMeshFromTopology(*mesh, assignments.WorkerID, detail)
		if err != nil {
			continue
		}
		SetMeshPlan(plan)
		return plan, nil
	}
	return ResolvedMesh{}, fmt.Errorf("узел не входит в активную топологию")
}

// MeshForStage строит настройки стадии (compute) для запуска RPC-сервера.
//
// Стадия не обслуживает задания, но должна держать ggml-rpc-server поднятым:
// координатор подключается к нему при загрузке слоёв.
func MeshForStage(cfg workercfg.Config) (workercfg.Mesh, error) {
	base := MeshOptionsOf(cfg)
	if base == nil {
		return workercfg.Mesh{}, fmt.Errorf("шард-режим не настроен")
	}
	if base.Role == meshRoleAuto {
		plan, ok := CurrentMeshPlan()
		if !ok {
			return *base, fmt.Errorf("план шард-группы ещё не получен с платформы")
		}
		if plan.Role != meshRoleCompute {
			return *base, fmt.Errorf("узел не стадия, а %s", plan.Role)
		}
		resolved := *base
		resolved.Role = plan.Role
		resolved.Layers = plan.Layers
		resolved.TopologyID = plan.TopologyID
		resolved.SelfWorkerID = plan.SelfWorkerID
		resolved.CoordinatorID = plan.CoordinatorID
		return resolved, nil
	}
	if base.Role != meshRoleCompute {
		return withDraftFromConfig(cfg, *base), fmt.Errorf("роль %s не стадия", base.Role)
	}
	return withDraftFromConfig(cfg, *base), nil
}

// MeshForJob строит настройки стадии для выполнения задания.
//
// Для роли auto берётся план с платформы: если узел оказался стадией, а не
// координатором, задание отклоняется — обслуживать чат он не может.
func MeshForJob(cfg workercfg.Config) (workercfg.Mesh, error) {
	base := MeshOptionsOf(cfg)
	if base == nil {
		return workercfg.Mesh{}, fmt.Errorf("шард-режим не настроен")
	}
	if base.Role != meshRoleAuto {
		return withDraftFromConfig(cfg, *base), nil
	}

	plan, ok := CurrentMeshPlan()
	if !ok {
		return *base, fmt.Errorf("план шард-группы ещё не получен с платформы")
	}
	if plan.Role != meshRoleCoordinator {
		return *base, fmt.Errorf("узел назначен стадией %s в топологии и не обслуживает чат", plan.Role)
	}
	if len(plan.Missing) > 0 {
		return *base, fmt.Errorf("соисполнители не объявили адрес RPC: %s",
			strings.Join(plan.Missing, ", "))
	}

	resolved := *base
	resolved.Role = plan.Role
	resolved.Layers = plan.Layers
	resolved.TopologyID = plan.TopologyID
	resolved.SelfWorkerID = plan.SelfWorkerID
	resolved.CoordinatorID = plan.CoordinatorID
	if plan.Model != "" {
		resolved.Model = plan.Model
	}
	// Прямые адреса идут в --rpc как есть; для узлов без адреса поднимается
	// локальный порт-туннель, поэтому их идентификаторы передаются отдельно.
	resolved.Peers = nil
	resolved.PeerWorkers = nil
	for _, peer := range plan.Peers {
		if peer.RPCAddr != "" && !base.Tunnel {
			resolved.Peers = append(resolved.Peers, peer.RPCAddr)
			continue
		}
		resolved.PeerWorkers = append(resolved.PeerWorkers, peer.WorkerID)
	}
	return withDraftFromConfig(cfg, resolved), nil
}

// meshTail запоминает хвост вывода процесса: по нему видно, почему рантайм
// не поднялся, без чтения файлов вручную.
type meshTail struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *meshTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.limit == 0 {
		t.limit = 8 << 10
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *meshTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// meshRuntime — запущенная стадия шард-группы.
type meshRuntime struct {
	cmd      *exec.Cmd
	waited   chan error
	role     string
	endpoint string
	digest   string
	model    string
	layers   string
	tail     *meshTail
	log      *slog.Logger
	started  time.Time
	sink     io.Closer
	// dialers — локальные порты-туннели к стадиям без прямого адреса.
	dialers []*MeshTunnelDialer
	// listener — исходящая сторона стадии, если связь идёт туннелем.
	listener *MeshTunnelListener
	// remote — в группе есть стадия за туннелем: старт долгий.
	remote bool
}

// meshProcess — текущий рантайм агента. Шард-группа одна на процесс: окно
// контекста и раскладка слоёв фиксированы, поэтому второй рантайм только
// поделил бы VRAM с первым.
var meshProcess struct {
	sync.Mutex
	rt      *meshRuntime
	configK string
}

// MeshMemoryPlan — сколько ресурсов нужно шард-группе и сколько есть.
type MeshMemoryPlan struct {
	WeightsBytes    uint64 `json:"weights_bytes"`
	NeededBytes     uint64 `json:"needed_bytes"`
	FreePhysBytes   uint64 `json:"free_phys_bytes"`
	FreeCommitBytes uint64 `json:"free_commit_bytes"`
	NeededVRAMBytes uint64 `json:"needed_vram_bytes"`
	FreeVRAMBytes   uint64 `json:"free_vram_bytes"`
	VRAMKnown       bool   `json:"vram_known"`
	OK              bool   `json:"ok"`
}

// meshMemoryNeed оценивает, сколько свободной памяти нужно координатору.
//
// Почему не «размер файла целиком»: веса читаются через mmap, и их страницы
// ядро отдаёт под давлением; не хватает не их, а буферов вычисления и
// KV-кэша. Наблюдённые точки на этой машине (файл 5 ГБ): при 1.1 ГБ свободной
// RAM llama-server падал на «Memory allocation failure», при 6–10 ГБ работал.
// Оценка ниже сознательно между ними: weights/2 + 2 ГБ.
func meshMemoryNeed(weightsBytes uint64) uint64 {
	need := weightsBytes/2 + 2<<30
	if min := uint64(3) << 30; need < min {
		need = min
	}
	return need
}

// meshVRAMNeed оценивает, сколько видеопамяти нужно координатору.
//
// Слои делятся между его картой и стадиями, поэтому целиком модель в VRAM не
// нужна. Наблюдённые отказы: при 1.7 ГБ свободной VRAM (Ollama держала модель
// на 5.7 ГБ) группа поднималась, но падала на первом же токене с
// «Remote RPC server crashed»; при 6 ГБ свободной VRAM тот же запуск проходил.
func meshVRAMNeed(weightsBytes uint64) uint64 {
	need := weightsBytes/2 + 1<<30
	// На карте 8 ГБ рабочий стол занимает около 1 ГБ, поэтому 7,6 ГБ свободными
	// не бывают. Запуск проходил при 6 ГБ свободных и падал при 1,7 ГБ,
	// когда модель ещё держала Ollama.
	const observedOK = uint64(6) << 30
	if need > observedOK {
		need = observedOK
	}
	if min := uint64(2) << 30; need < min {
		need = min
	}
	return need
}

// PlanMeshMemory проверяет, хватит ли памяти на запуск координатора.
//
// Проверяются оба ресурса, и это не формальность: видеопамять машины делят все
// процессы, и Ollama с загруженной моделью оставляет новой группе столько,
// что та поднимается и умирает на первом токене.
func PlanMeshMemory(ctx context.Context, ggufPath string) (MeshMemoryPlan, error) {
	info, err := os.Stat(ggufPath)
	if err != nil {
		return MeshMemoryPlan{}, fmt.Errorf("mesh: не читается файл весов %s: %w", ggufPath, err)
	}
	weights := uint64(info.Size())
	plan := MeshMemoryPlan{
		WeightsBytes:    weights,
		NeededBytes:     meshMemoryNeed(weights),
		NeededVRAMBytes: meshVRAMNeed(weights),
	}

	if freePhys, freeCommit, memErr := MemoryStatus(); memErr == nil {
		plan.FreePhysBytes = freePhys
		plan.FreeCommitBytes = freeCommit
	}

	if freeVRAM, known := VRAMFree(ctx); known && freeVRAM > 0 {
		plan.VRAMKnown = true
		plan.FreeVRAMBytes = uint64(freeVRAM)
	} else {
		// Видеокарты может не быть: тогда весь расчёт — про память хоста.
		plan.NeededVRAMBytes = 0
	}

	plan.OK = plan.FreePhysBytes >= plan.NeededBytes &&
		(!plan.VRAMKnown || plan.FreeVRAMBytes >= plan.NeededVRAMBytes)
	return plan, nil
}

// ensureMeshMemory освобождает память и проверяет, что группе её хватит.
//
// Сначала выгружаем модели Ollama: на узле с шард-группой чат обслуживает
// llama.cpp, а Ollama нужна только для эмбеддингов. Это дешёвый способ вернуть
// и RAM, и VRAM, вместо того чтобы падать в CUDA на первом токене.
func ensureMeshMemory(ctx context.Context, ggufPath string, log *slog.Logger) error {
	plan, err := PlanMeshMemory(ctx, ggufPath)
	if err != nil {
		return err
	}
	if plan.OK {
		log.Info("mesh memory check passed",
			"need_ram_mb", plan.NeededBytes>>20, "free_ram_mb", plan.FreePhysBytes>>20,
			"need_vram_mb", plan.NeededVRAMBytes>>20, "free_vram_mb", plan.FreeVRAMBytes>>20)
		return nil
	}

	if unloaded, unloadErr := UnloadOllamaModels(ctx); unloadErr != nil {
		log.Warn("mesh: cannot unload runtime models", "err", unloadErr)
	} else if len(unloaded) > 0 {
		log.Info("mesh: runtime models unloaded to free memory", "models", unloaded)
		if retry, retryErr := PlanMeshMemory(ctx, ggufPath); retryErr == nil {
			if retry.OK {
				log.Info("mesh memory check passed after unload",
					"need_ram_mb", retry.NeededBytes>>20, "free_ram_mb", retry.FreePhysBytes>>20,
					"need_vram_mb", retry.NeededVRAMBytes>>20, "free_vram_mb", retry.FreeVRAMBytes>>20)
				return nil
			}
			plan = retry
		}
	}

	if plan.VRAMKnown && plan.FreeVRAMBytes < plan.NeededVRAMBytes {
		return fmt.Errorf(
			"insufficient GPU memory for the shard group: need about %d MiB free VRAM, have %d MiB "+
				"(another process holds the card — close it or stop the local model)",
			plan.NeededVRAMBytes>>20, plan.FreeVRAMBytes>>20)
	}
	return fmt.Errorf(
		"insufficient memory for the shard group: need about %d MiB free RAM, have %d MiB "+
			"(weights %d MiB, commit headroom %d MiB); close other programs or use a smaller model",
		plan.NeededBytes>>20, plan.FreePhysBytes>>20, plan.WeightsBytes>>20, plan.FreeCommitBytes>>20)
}

// EnsureMeshRuntime поднимает стадию шард-группы, если она ещё не работает.
//
// Повторный вызов с той же конфигурацией возвращает существующий рантайм:
// загрузка слоёв занимает десятки секунд, и на каждое задание её повторять
// нельзя.
func EnsureMeshRuntime(ctx context.Context, client *Client, m workercfg.Mesh, log *slog.Logger) (*meshRuntime, error) {
	m = MeshNormalize(m)
	key := meshConfigKey(m)

	meshProcess.Lock()
	defer meshProcess.Unlock()

	if meshProcess.rt != nil && meshProcess.configK == key {
		if meshProcess.rt.alive() {
			return meshProcess.rt, nil
		}
		meshProcess.rt.stop()
		meshProcess.rt = nil
		meshProcess.configK = ""
	}
	if meshProcess.rt != nil {
		meshProcess.rt.stop()
		meshProcess.rt = nil
		meshProcess.configK = ""
	}

	rt, err := startMeshRuntime(ctx, client, m, log)
	if err != nil {
		return nil, err
	}
	meshProcess.rt = rt
	meshProcess.configK = key
	return rt, nil
}

// StopMeshRuntime гасит стадию шард-группы, если она запущена.
func StopMeshRuntime() {
	meshProcess.Lock()
	defer meshProcess.Unlock()
	if meshProcess.rt != nil {
		meshProcess.rt.stop()
		meshProcess.rt = nil
		meshProcess.configK = ""
	}
}

// MeshStatusInfo — состояние шард-группы для панели, heartbeat и CLI.
type MeshStatusInfo struct {
	Running  bool   `json:"running"`
	Role     string `json:"role"`
	Model    string `json:"model,omitempty"`
	Layers   string `json:"layers,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Digest   string `json:"digest,omitempty"`
	UptimeMS int64  `json:"uptime_ms,omitempty"`
}

// MeshStatus сообщает, что сейчас работает.
func MeshStatus() MeshStatusInfo {
	meshProcess.Lock()
	defer meshProcess.Unlock()
	if meshProcess.rt == nil || !meshProcess.rt.alive() {
		return MeshStatusInfo{}
	}
	rt := meshProcess.rt
	return MeshStatusInfo{
		Running:  true,
		Role:     rt.role,
		Model:    rt.model,
		Layers:   rt.layers,
		Endpoint: rt.endpoint,
		Digest:   rt.digest,
		UptimeMS: time.Since(rt.started).Milliseconds(),
	}
}

// meshConfigKey — отпечаток конфигурации: при его смене рантайм перезапускается.
func meshConfigKey(m workercfg.Mesh) string {
	return strings.Join([]string{
		m.Role, m.Model, m.GGUFPath, m.Layers, strings.Join(m.Peers, ","),
		strings.Join(m.PeerWorkers, ","), m.TopologyID, m.SelfWorkerID,
		fmt.Sprint(m.ServerPort, m.RPCPort, m.Ctx, m.Threads, m.ListenHost, m.Cache),
	}, "|")
}

// alive проверяет, что процесс ещё работает и, для координатора, что порт жив.
//
// У стадии порт не опрашивается намеренно: ggml-rpc-server обслуживает одно
// соединение за раз, и посторонний TCP-опрос заставляет его закрыть активную
// сессию. Наблюдённый отказ: координатор получал «Remote RPC server crashed»
// ровно в тот момент, когда цикл заданий стадии проверял её живость.
func (r *meshRuntime) alive() bool {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return false
	}
	if !r.processAlive() {
		return false
	}
	if r.role == meshRoleCompute {
		return true
	}
	conn, err := net.DialTimeout("tcp", r.probeAddr(), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// processAlive сообщает, что дочерний процесс ещё не завершился.
func (r *meshRuntime) processAlive() bool {
	select {
	case <-r.waited:
		return false
	default:
		return true
	}
}

// probeAddr — адрес, по которому видно, что рантайм жив.
func (r *meshRuntime) probeAddr() string {
	if r.role == meshRoleCoordinator {
		return strings.TrimPrefix(r.endpoint, "http://")
	}
	return r.endpoint
}

// killMeshRuntime помечает рантайм мёртвым, чтобы следующее задание подняло
// его заново. Вызывается после падения во время работы.
func killMeshRuntime() {
	meshProcess.Lock()
	defer meshProcess.Unlock()
	if meshProcess.rt != nil {
		meshProcess.rt.stop()
		meshProcess.rt = nil
		meshProcess.configK = ""
	}
}

// stop гасит процесс и ждёт его завершения.
func (r *meshRuntime) stop() {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	// Туннели закрываются первыми: иначе llama-server может успеть открыть
	// новое соединение к уже умирающей стадии.
	for _, dialer := range r.dialers {
		dialer.Close()
	}
	r.dialers = nil
	if r.listener != nil {
		r.listener.Close()
		r.listener = nil
	}
	_ = r.cmd.Process.Kill()
	select {
	case <-r.waited:
	case <-time.After(10 * time.Second):
	}
	if r.sink != nil {
		_ = r.sink.Close()
		r.sink = nil
	}
	r.log.Info("mesh runtime stopped", "role", r.role)
}

// startMeshRuntime запускает процесс стадии и ждёт его готовности.
func startMeshRuntime(ctx context.Context, client *Client, m workercfg.Mesh, log *slog.Logger) (*meshRuntime, error) {
	dir := m.LlamaDir
	if dir == "" {
		return nil, fmt.Errorf("mesh: llama_dir is not set: укажите каталог с llama-server")
	}

	var (
		exe    string
		args   []string
		dialer []*MeshTunnelDialer
	)

	switch m.Role {
	case meshRoleCompute:
		exe = filepath.Join(dir, executableName("ggml-rpc-server"))
		if m.ListenHost != "127.0.0.1" && m.ListenHost != "localhost" {
			log.Warn("mesh: RPC port is exposed beyond loopback; "+
				"protocol has no TLS or auth — use a tunnel", "listen_host", m.ListenHost)
		}
		args = []string{"-H", m.ListenHost, "-p", fmt.Sprint(m.RPCPort), "-d", "CPU"}
		if m.Threads > 0 {
			args = append(args, "-t", fmt.Sprint(m.Threads))
		}
		if m.Cache {
			args = append(args, "-c")
		}
	case meshRoleCoordinator:
		exe = filepath.Join(dir, executableName("llama-server"))
		if m.GGUFPath == "" {
			return nil, fmt.Errorf("mesh: gguf_path is not set: координатору нужен локальный файл весов")
		}
		if _, err := os.Stat(m.GGUFPath); err != nil {
			return nil, fmt.Errorf("mesh: не читается файл весов %s: %w", m.GGUFPath, err)
		}
		args = []string{
			"-m", m.GGUFPath,
			"-c", fmt.Sprint(m.Ctx),
			"-np", "1",
			"--host", "127.0.0.1",
			"--port", fmt.Sprint(m.ServerPort),
			"--no-webui",
			"--metrics",
		}
		// Соисполнители без прямого адреса доступны через туннель: локальный
		// порт-прокси выглядит для llama-server как обычный RPC-сервер.
		rpcAddrs := append([]string{}, m.Peers...)
		for _, workerID := range m.PeerWorkers {
			tunnel, err := StartMeshTunnelDialer(ctx, client, m.TopologyID, workerID, m.SelfWorkerID)
			if err != nil {
				log.Warn("mesh: cannot open tunnel to stage", "worker_id", workerID, "err", err)
				continue
			}
			dialer = append(dialer, tunnel)
			rpcAddrs = append(rpcAddrs, tunnel.Addr())
			log.Info("mesh tunnel ready", "worker_id", workerID, "local_addr", tunnel.Addr())
		}
		if len(rpcAddrs) > 0 {
			args = append(args, "--rpc", strings.Join(rpcAddrs, ","))
		}
		// -ngl 99 означает «все слои на устройства»: локальная карта и
		// RPC-стадии делят их по свободной памяти или по явным долям.
		args = append(args, "-ngl", "99")
		if len(m.Split) > 0 {
			parts := make([]string, 0, len(m.Split))
			for _, share := range m.Split {
				parts = append(parts, fmt.Sprintf("%g", share))
			}
			args = append(args, "-ts", strings.Join(parts, ","))
		}
		if m.Threads > 0 {
			args = append(args, "-t", fmt.Sprint(m.Threads))
		}
		if m.NCPUMoE > 0 {
			args = append(args, "--n-cpu-moe", fmt.Sprint(m.NCPUMoE))
		}
		if m.DraftGGUFPath != "" {
			if _, err := os.Stat(m.DraftGGUFPath); err == nil {
				spec := m.SpecType
				if spec == "" {
					spec = DefaultDraftSpec
				}
				args = append(args, "-md", m.DraftGGUFPath, "--spec-type", spec)
			}
		}
	default:
		return nil, fmt.Errorf("mesh: unknown role %q", m.Role)
	}

	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("mesh: не найден %s: %w", exe, err)
	}

	if m.Tunnel && client == nil {
		return nil, fmt.Errorf("mesh: для туннеля нужен клиент платформы (control_url и секрет устройства)")
	}

	// Проверяем память до запуска: без этого нехватка RAM выглядит как
	// падение llama.cpp с «CUDA error», и клиент не понимает причины.
	if m.Role == meshRoleCoordinator {
		if err := ensureMeshMemory(ctx, m.GGUFPath, log); err != nil {
			return nil, err
		}
	}

	cmd := quiet(exec.Command(exe, args...))
	cmd.Dir = dir
	tail := &meshTail{}
	// Вывод рантайма нужен и в памяти (для текста ошибки), и в файле: разбор
	// раскладки слоёв и transfer time возможен только по логу llama.cpp.
	var sink io.Writer = tail
	var sinkCloser io.Closer
	if logPath := meshLogPath(m.Role); logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			// Файл закрывается вместе с рантаймом, а не при выходе из этой
			// функции: иначе потомок останется с закрытым дескриптором.
			sinkCloser = file
			sink = io.MultiWriter(tail, file)
			log.Info("mesh runtime log", "path", logPath)
		} else {
			log.Warn("mesh: cannot open runtime log", "err", err)
		}
	}
	cmd.Stdout = sink
	cmd.Stderr = sink

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mesh: cannot start %s: %w", filepath.Base(exe), err)
	}

	rt := &meshRuntime{
		cmd:     cmd,
		waited:  make(chan error, 1),
		role:    m.Role,
		model:   m.Model,
		layers:  m.Layers,
		tail:    tail,
		log:     log,
		started: time.Now(),
		sink:    sinkCloser,
		dialers: dialer,
		remote:  len(m.PeerWorkers) > 0,
	}
	if m.Role == meshRoleCoordinator {
		rt.endpoint = fmt.Sprintf("http://127.0.0.1:%d", m.ServerPort)
	} else {
		rt.endpoint = net.JoinHostPort(m.ListenHost, fmt.Sprint(m.RPCPort))
	}

	go func() { rt.waited <- cmd.Wait() }()

	log.Info("mesh runtime starting",
		"role", m.Role, "exe", filepath.Base(exe), "endpoint", rt.endpoint,
		"layers", m.Layers, "peers", m.Peers, "args", strings.Join(args, " "))

	if err := rt.waitReady(ctx); err != nil {
		rt.stop()
		return nil, err
	}

	if m.Role == meshRoleCoordinator {
		// Digest весов считается по локальному файлу: это то же доказательство,
		// что и для Ollama, и оно не зависит от того, что рантайм о себе думает.
		digest, err := fileDigest(m.GGUFPath)
		if err != nil {
			log.Warn("mesh: cannot hash weights", "err", err, "path", m.GGUFPath)
		}
		rt.digest = digest
	} else if m.Tunnel {
		// Стадия сама идёт на платформу: входящих соединений у неё нет, порт
		// RPC слушает только loopback.
		if m.TopologyID == "" || m.SelfWorkerID == "" {
			rt.stop()
			return nil, fmt.Errorf("mesh: туннель требует topology_id и self_worker_id")
		}
		listener, err := StartMeshTunnelListener(ctx, client, m.TopologyID, m.SelfWorkerID, m.CoordinatorID, rt.endpoint)
		if err != nil {
			rt.stop()
			return nil, err
		}
		rt.listener = listener
		log.Info("mesh tunnel listener started",
			"topology", m.TopologyID, "worker_id", m.SelfWorkerID, "rpc_addr", rt.endpoint)
	}

	log.Info("mesh runtime ready", "role", m.Role, "endpoint", rt.endpoint,
		"digest", rt.digest, "elapsed_ms", time.Since(rt.started).Milliseconds())
	return rt, nil
}

// waitReady ждёт, пока стадия начнёт отвечать, и следит за падением процесса.
func (r *meshRuntime) waitReady(ctx context.Context) error {
	// С удалённой стадией тензоры идут по сети: при домашней отдаче это
	// десятки минут на первый запуск, поэтому ждём дольше, чем для локальной.
	ready := meshReadyTimeout
	if r.remote {
		ready = meshRemoteReadyTimeout
	}
	deadline := time.Now().Add(ready)
	client := &http.Client{Timeout: 2 * time.Second}
	attempts := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case err := <-r.waited:
			// Процесс умер: канал уже прочитан, возвращаем его обратно,
			// чтобы alive() и stop() увидели завершение.
			r.waited <- err
			return fmt.Errorf("mesh runtime exited during startup: %v; %s", err, r.tailLast())
		default:
		}

		switch r.role {
		case meshRoleCoordinator:
			resp, err := client.Get(r.endpoint + "/health")
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
				// 503 во время загрузки слоёв — норма; логируем редко, чтобы
				// по журналу было видно, на чём именно застрял старт.
				if attempts%20 == 0 {
					r.log.Info("mesh runtime not ready yet", "health", resp.StatusCode)
				}
			} else if attempts%20 == 0 {
				r.log.Info("mesh runtime health check failed", "err", err)
			}
		default:
			conn, err := net.DialTimeout("tcp", r.endpoint, 2*time.Second)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("mesh runtime did not become ready in %s; %s",
				meshReadyTimeout, r.tailLast())
		}
		attempts++
		time.Sleep(500 * time.Millisecond)
	}
}

// tailLast возвращает хвост вывода процесса для текста ошибки.
func (r *meshRuntime) tailLast() string {
	if r.tail == nil {
		return ""
	}
	out := r.tail.String()
	if out == "" {
		return "no output"
	}
	return out
}

// fileDigest считает sha256 файла, не загружая его в память.
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// executableName добавляет .exe на Windows.
func executableName(base string) string {
	if filepath.Separator == '\\' {
		return base + ".exe"
	}
	return base
}

// meshLogPath — куда писать вывод llama.cpp. Без файла разбор раскладки слоёв
// и времени передачи тензоров невозможен: сервер печатает их только в лог.
func meshLogPath(role string) string {
	dir := os.Getenv("COMPUTE_NETWORK_LOG_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "compute-mesh-"+role+".log")
}

// RunMeshChatJob выполняет чат-задание через шардированную модель.
//
// Контракт с клиентом тот же, что у обычного задания: чанки уходят потоком на
// Control Plane. Отличие только в источнике — llama-server координатора, а не
// Ollama, и в доказательстве весов: digest считается по GGUF-файлу.
func RunMeshChatJob(ctx context.Context, client *Client, job *Job, m workercfg.Mesh, numCtx int, log *slog.Logger) JobResult {
	fail := func(msg string) JobResult { return JobResult{Error: msg} }
	m = MeshNormalize(m)
	// Окно задаёт шард-группа: llama-server запущен с конкретным -c, и
	// сравнивать промпт нужно с ним, а не с настройкой Ollama-узла.
	if m.Ctx > 0 {
		numCtx = m.Ctx
	}
	if numCtx <= 0 {
		numCtx = DefaultNumCtx
	}

	var request ChatRequest
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &request); err != nil {
			return fail("cannot parse job payload: " + err.Error())
		}
	}
	model := request.Model
	if model == "" {
		model = job.Model
	}
	if model == "" {
		model = m.Model
	}

	if minTokens := MinPromptTokens(request); minTokens > numCtx {
		return fail(fmt.Sprintf(
			"prompt does not fit the shard-group context window: at least %d tokens against %d",
			minTokens, numCtx))
	}

	rt, err := EnsureMeshRuntime(ctx, client, m, log)
	if err != nil {
		return fail("shard group unavailable: " + err.Error())
	}

	body := map[string]any{
		"model":       model,
		"messages":    request.Messages,
		"stream":      true,
		"max_tokens":  0,
		"temperature": 0.7,
	}
	if request.MaxTokens > 0 {
		body["max_tokens"] = request.MaxTokens
	}
	if request.Temperature != nil {
		body["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		body["top_p"] = *request.TopP
	}
	if request.Seed != nil {
		body["seed"] = *request.Seed
	}
	if len(request.Stop) > 0 && string(request.Stop) != "null" {
		body["stop"] = request.Stop
	}
	if len(request.Tools) > 0 {
		body["tools"] = request.Tools
	}
	// usage приходит только по запросу: без него нельзя ни учесть токены,
	// ни поймать молчаливую обрезку контекста.
	body["stream_options"] = map[string]any{"include_usage": true}

	raw, err := json.Marshal(body)
	if err != nil {
		return fail("cannot encode request: " + err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.endpoint+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return fail("cannot build runtime request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Порт не отвечает: группа умерла. Гасим её, чтобы следующее задание
		// подняло стадии заново, а не уходило в мёртвый рантайм.
		killMeshRuntime()
		return fail("shard group unavailable: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail(fmt.Sprintf("shard runtime http %d: %s", resp.StatusCode, strings.TrimSpace(string(text))))
	}

	stream, err := client.ChunkStream(ctx, job.ID)
	if err != nil {
		return fail("cannot open chunk stream: " + err.Error())
	}

	started := time.Now()
	var (
		chunks       int
		contentBytes int
		toolCalls    int
		promptTokens int
		outputTokens int
		evalMS       int64
		finish       string
	)

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		if ctx.Err() != nil {
			stream.Abort(ctx.Err())
			return fail("cancelled")
		}
		line, readErr := reader.ReadBytes('\n')

		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if bytes.Equal(payload, []byte("[DONE]")) {
				break
			}
			var event struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						ToolCalls        []struct {
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
				Timings *struct {
					PromptN     int     `json:"prompt_n"`
					PredictedN  int     `json:"predicted_n"`
					PredictedMS float64 `json:"predicted_ms"`
				} `json:"timings"`
			}
			if err := json.Unmarshal(payload, &event); err == nil {
				if event.Usage != nil {
					promptTokens = event.Usage.PromptTokens
					outputTokens = event.Usage.CompletionTokens
				}
				if event.Timings != nil {
					evalMS = int64(event.Timings.PredictedMS)
					if event.Timings.PredictedN > 0 {
						outputTokens = event.Timings.PredictedN
					}
					if event.Timings.PromptN > 0 {
						promptTokens = event.Timings.PromptN
					}
				}
				for _, choice := range event.Choices {
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						finish = *choice.FinishReason
					}
					piece := chunkPiece{
						Content:  choice.Delta.Content,
						Thinking: choice.Delta.ReasoningContent,
					}
					for _, call := range choice.Delta.ToolCalls {
						arguments := call.Function.Arguments
						if arguments == "" {
							arguments = "{}"
						}
						piece.ToolCalls = append(piece.ToolCalls, chunkCall{
							Name:      call.Function.Name,
							Arguments: arguments,
						})
					}
					if piece.Content == "" && piece.Thinking == "" && len(piece.ToolCalls) == 0 {
						continue
					}
					if err := stream.Send(piece); err != nil {
						stream.Abort(err)
						return fail("chunk delivery failed: " + err.Error())
					}
					chunks++
					contentBytes += len(piece.Content)
					toolCalls += len(piece.ToolCalls)
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			stream.Abort(readErr)
			// Ответ оборвался на середине: рантайм упал. Метим группу мёртвой,
			// иначе следующее задание снова получит «connection reset».
			killMeshRuntime()
			return fail("shard runtime stream error: " + readErr.Error())
		}
	}

	result := JobResult{
		PromptTokens:     promptTokens,
		CompletionTokens: outputTokens,
		EvalMS:           evalMS,
		FinishReason:     finish,
		ServedModel:      rt.model,
		ServedDigest:     rt.digest,
	}
	if result.FinishReason == "" {
		result.FinishReason = "stop"
	}
	if toolCalls > 0 {
		result.FinishReason = "tool_calls"
	}
	if promptTruncated(request, promptTokens) || (numCtx > 0 && promptTokens >= numCtx-16) {
		result.Error = fmt.Sprintf(
			"prompt does not fit the shard-group context window (%d tokens): "+
				"the beginning of the conversation was dropped", numCtx)
	}

	if err := stream.Close(); err != nil {
		if result.Error == "" {
			result.Error = "cannot deliver answer: " + err.Error()
		}
		log.Warn("chunk stream closed with error", "job", job.ID, "err", err)
		return result
	}

	log.Info("mesh job finished",
		"job", job.ID, "model", result.ServedModel, "role", rt.role,
		"chunks", chunks, "content_bytes", contentBytes, "tool_calls", toolCalls,
		"tokens_out", result.CompletionTokens, "finish", result.FinishReason,
		"generation_ms", result.EvalMS,
		"wall_ms", time.Since(started).Milliseconds())
	return result
}
