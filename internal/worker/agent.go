package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/private-compute/network/internal/store"
	"github.com/private-compute/network/internal/workercfg"
)

// AgentVersion — версия агента, отправляется при регистрации и в heartbeat.
// Её же программа сравнивает с release.json на платформе.
const AgentVersion = "0.3.3"

// ActiveJobs — сколько заданий узел выполняет прямо сейчас.
// Значение уходит в heartbeat, чтобы панель показывала реальную загрузку.
var ActiveJobs atomic.Int32

// meshAutoLogged не даёт повторять одно и то же сообщение на каждом heartbeat.
var meshAutoLogged atomic.Bool

const (
	minBackoff     = 1 * time.Second
	maxBackoff     = 5 * time.Minute
	fallbackPeriod = 30 * time.Second
)

// Enroll обнаруживает железо, регистрирует узел и сохраняет конфигурацию.
func Enroll(ctx context.Context, cfgPath, controlURL, token, name string, log *slog.Logger) (workercfg.Config, SystemInfo, error) {
	if controlURL == "" {
		return workercfg.Config{}, SystemInfo{}, errors.New("control URL is required")
	}
	if token == "" {
		return workercfg.Config{}, SystemInfo{}, errors.New("enrollment token is required")
	}

	log.Info("discovering hardware")
	info, err := Discover(ctx)
	if err != nil {
		return workercfg.Config{}, SystemInfo{}, fmt.Errorf("discover: %w", err)
	}
	if info.Hostname == "" {
		info.Hostname = name
	}
	if name == "" {
		name = info.Hostname
	}

	client := NewClient(controlURL, "")
	req := EnrollRequest{
		Token:        token,
		Name:         name,
		Hostname:     info.Hostname,
		OS:           info.OS,
		AgentVersion: AgentVersion,
		Hardware:     info.Capabilities(),
	}

	resp, err := client.Enroll(ctx, req)
	if err != nil {
		return workercfg.Config{}, SystemInfo{}, fmt.Errorf("enroll: %w", err)
	}

	cfg := workercfg.Config{
		ControlURL:       controlURL,
		WorkerName:       resp.Name,
		DeviceToken:      resp.DeviceToken,
		HeartbeatSeconds: 30,
		EnrolledAt:       time.Now().UTC(),
	}
	if err := workercfg.Save(cfgPath, cfg); err != nil {
		return workercfg.Config{}, SystemInfo{}, fmt.Errorf("save config: %w", err)
	}

	log.Info("worker enrolled",
		"worker_id", resp.WorkerID,
		"name", resp.Name,
		"state", resp.State,
		"trust_class", resp.TrustClass,
		"config", cfgPath,
	)
	return cfg, info, nil
}

// Run отправляет heartbeat с реконнектом и экспоненциальной задержкой.
func Run(ctx context.Context, cfgPath string, once bool, log *slog.Logger) error {
	backoff := minBackoff
	// RTT последнего успешного heartbeat: отправляем его серверу как измеренный
	// показатель качества линка (нужен Scheduler для прогноза скорости).
	var lastRTT time.Duration
	var disks []store.Disk
	client := NewClient("", "")

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		// Конфигурация перечитывается каждый проход: «pause» и «resume»,
		// выполненные из консоли, должны действовать без перезапуска агента.
		cfg, err := workercfg.Load(cfgPath)
		if err != nil {
			log.Warn("cannot read config, retrying", "err", err)
			if once {
				return err
			}
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		client.Retarget(cfg.ControlURL, cfg.DeviceToken)
		hadMesh := cfg.Mesh != nil
		cfg = prepareAutomaticMesh(cfg)
		if !hadMesh && cfg.Mesh != nil && meshAutoLogged.CompareAndSwap(false, true) {
			log.Info("шард-режим доступен: слои разделятся, когда в сети будет второй узел с llama.cpp",
				"llama_dir", cfg.Mesh.LlamaDir)
		}
		if cfg.Mesh == nil {
			startLlamaInstall(ctx, log)
		}

		// Список дисков нужен только как метаданные: обнаружение может быть недоступно.
		if disks == nil {
			if info, err := Discover(ctx); err == nil {
				disks = info.Disks
			} else {
				log.Warn("hardware discovery unavailable, continuing without disks", "err", err)
			}
		}

		period := time.Duration(cfg.HeartbeatSeconds) * time.Second
		if period <= 0 {
			period = fallbackPeriod
		}

		state := "available"
		if cfg.Paused {
			state = "paused"
		}

		tel, telErr := Collect(ctx, disks)
		if telErr != nil {
			log.Warn("telemetry unavailable, sending empty heartbeat", "err", telErr)
		}

		meta := tel.Meta
		if meta == nil {
			meta = map[string]any{}
		}
		// RTT предыдущего heartbeat: сервер использует его для прогноза скорости.
		if lastRTT > 0 {
			meta["rtt_ms"] = lastRTT.Milliseconds()
		}
		// Какую модель узел готов выполнять: по этому полю панель и API
		// отвечают, что реально можно вызвать, не выдумывая каталог.
		// Координатор шард-группы объявляет имя шард-модели, чтобы задания
		// на неё приходили именно ему, а не любому узлу с похожей локальной.
		served := cfg.Model
		if meshModel := MeshServedModel(cfg); meshModel != "" {
			served = meshModel
		}
		// Шард-режим виден платформе: роль, диапазон слоёв и состояние группы.
		if cfg.Mesh != nil {
			meta["mesh_capable"] = true
			if cfg.Model != "" {
				meta["mesh_model"] = cfg.Model
			}
		}
		if mesh := MeshOptionsOf(cfg); mesh != nil {
			role := mesh.Role
			if role == meshRoleAuto {
				// Роль и соисполнителей задаёт топология: спрашиваем платформу
				// на каждом heartbeat, чтобы смена раскладки применялась без
				// правки файлов на машинах.
				if plan, planErr := RefreshMeshPlan(ctx, client, cfg); planErr == nil {
					role = plan.Role
					meta["mesh_plan_source"] = plan.Source
					meta["mesh_topology_version"] = plan.Version
					if plan.Layers != "" {
						meta["mesh_layers"] = plan.Layers
					}
					if len(plan.Peers) > 0 {
						meta["mesh_peers"] = len(plan.Peers)
					}
					if len(plan.Missing) > 0 {
						meta["mesh_missing_stages"] = plan.Missing
					}
					if plan.Role == meshRoleCoordinator && plan.Model != "" {
						served = plan.Model
					}
				} else {
					log.Warn("cannot resolve shard plan from topology", "err", planErr)
				}
			}
			meta["mesh_role"] = role
			// Стадия не обслуживает клиентские запросы: объявлять её модель
			// значит отдавать ей задания, которые она выполнить не может.
			if role == meshRoleCompute {
				served = ""
			}
			if mesh.Layers != "" {
				meta["mesh_layers"] = mesh.Layers
			}
			if len(mesh.Peers) > 0 {
				meta["mesh_peers"] = len(mesh.Peers)
			}
			// Адрес стадии объявляется только когда узел реально отдаёт слои:
			// координатор своего RPC-сервера не держит.
			stage := *mesh
			stage.Role = role
			if addr := MeshPeerAddress(stage); addr != "" {
				meta["mesh_rpc_addr"] = addr
			}
			if status := MeshStatus(); status.Running {
				meta["mesh_running"] = true
				meta["mesh_endpoint"] = status.Endpoint
			}
		}
		if IsRetiredModel(served) {
			served = ""
		}
		meta["served_model"] = served
		if held := HeldPieceNames(workercfg.DefaultModelDir()); len(held) > 0 {
			meta["held_pieces"] = held
		}
		// Окно контекста узла: по нему клиент и панель понимают, сколько
		// токенов влезает в промпт, не дожидаясь молчаливой обрезки.
		meta["num_ctx"] = NumCtxOf(cfg)
		// Модель эмбеддингов: отдельная от чата, иначе узел не может
		// обслуживать оба вида заданий одновременно.
		if embedModel := EmbeddingModelOf(cfg); embedModel != "" {
			meta["served_embedding_model"] = embedModel
		}
		// Какие веса узел держит локально: digest'ы из рантайма. По ним платформа
		// сверяет модель с манифестом каталога.
		if digests := LocalModelDigests(ctx); len(digests) > 0 {
			meta["model_digests"] = digests
			// Самопроверка: узел пересчитывает sha256 файла весов сам, а не
			// только повторяет то, что сказал рантайм.
			selfVerified := make(map[string]bool, len(digests))
			for model, digest := range digests {
				if VerifyLocalWeights(ctx, model, digest) {
					selfVerified[model] = true
				}
			}
			if len(selfVerified) > 0 {
				meta["model_self_verified"] = selfVerified
			}
		}

		req := HeartbeatRequest{
			State:         state,
			CPUPercent:    tel.CPUPercent,
			RAMFreeBytes:  tel.RAMFreeBytes,
			VRAMFreeBytes: tel.VRAMFreeBytes,
			DiskFreeBytes: tel.DiskFreeBytes,
			TemperatureC:  tel.TemperatureC,
			PowerWatts:    tel.PowerWatts,
			ActiveJobs:    Ptr(int(ActiveJobs.Load())),
			Meta:          meta,
		}

		httpStarted := time.Now()
		resp, err := client.Heartbeat(ctx, req)
		rtt := time.Since(httpStarted)
		switch {
		case err != nil:
			log.Warn("heartbeat failed, will retry",
				"err", err,
				"retry_in", backoff.String(),
			)
			if once {
				return fmt.Errorf("heartbeat: %w", err)
			}
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue

		default:
			backoff = minBackoff
			if rtt > 0 {
				lastRTT = rtt
			}
			if resp.IntervalSecs > 0 {
				period = time.Duration(resp.IntervalSecs) * time.Second
			}
			attrs := []any{
				"worker_id", resp.WorkerID,
				"state", state,
				"desired_state", resp.DesiredState,
				"rtt_ms", rtt.Milliseconds(),
			}
			if tel.VRAMFreeBytes != nil {
				attrs = append(attrs, "vram_free_mb", *tel.VRAMFreeBytes/1024/1024)
			}
			if tel.TemperatureC != nil {
				attrs = append(attrs, "gpu_temp_c", *tel.TemperatureC)
			}
			log.Info("heartbeat ok", attrs...)

			if resp.DesiredState == "drain" || resp.DesiredState == "paused" || resp.DesiredState == "quarantined" {
				log.Warn("control plane requests stop", "desired_state", resp.DesiredState)
			} else if resp.Weights != nil && (len(resp.Weights.Fetch) > 0 || len(resp.Weights.Release) > 0) {
				dir := workercfg.DefaultModelDir()
				if resp.Weights.RevisionID != "" {
					dir = filepath.Join(dir, resp.Weights.RevisionID)
				}
				if err := SyncWeights(ctx, dir, resp.Weights.Fetch, resp.Weights.Release); err != nil {
					log.Warn("weight sync failed", "err", err, "model", resp.Weights.Model)
				} else {
					log.Info("weights updated",
						"model", resp.Weights.Model,
						"fetch", len(resp.Weights.Fetch),
						"release", len(resp.Weights.Release),
						"short", len(resp.Weights.Short),
						"missing", len(resp.Weights.Missing),
					)
				}
			}
			if strings.EqualFold(cfg.Model, DefaultChatModel) && (cfg.DraftGGUFPath == "" || !fileExists(cfg.DraftGGUFPath)) {
				go rememberDraftHead(ctx, cfgPath, log)
			}
			go followDesiredModel(ctx, cfgPath, cfg.Model, log)
			if once {
				return nil
			}
		}

		if !sleep(ctx, period) {
			return nil
		}
	}
}

var draftPull struct {
	mu   sync.Mutex
	busy bool
	last time.Time
}

// rememberDraftHead докачивает голову, если прошлый запуск не смог создать каталог.
// Повтор не чаще раза в десять минут и не параллельно с самим собой.
func rememberDraftHead(ctx context.Context, cfgPath string, log *slog.Logger) {
	draftPull.mu.Lock()
	if draftPull.busy || time.Since(draftPull.last) < 10*time.Minute {
		draftPull.mu.Unlock()
		return
	}
	draftPull.busy = true
	draftPull.last = time.Now()
	draftPull.mu.Unlock()
	defer func() {
		draftPull.mu.Lock()
		draftPull.busy = false
		draftPull.mu.Unlock()
	}()

	path, err := FetchDraftHead(ctx, nil)
	if err != nil {
		log.Warn("draft head was not downloaded", "err", err)
		return
	}
	cfg, err := workercfg.Load(cfgPath)
	if err != nil {
		log.Warn("cannot store draft head path", "err", err)
		return
	}
	if cfg.DraftGGUFPath == path {
		return
	}
	cfg.DraftGGUFPath = path
	if err := workercfg.Save(cfgPath, cfg); err != nil {
		log.Warn("cannot store draft head path", "err", err)
		return
	}
	log.Info("draft head is ready", "path", path)
}

var modelPull struct {
	mu   sync.Mutex
	busy bool
}

// followDesiredModel скачивает веса, если платформа сменила модель.
// Пока качается одна модель, второй запуск не стартует.
func followDesiredModel(ctx context.Context, cfgPath, current string, log *slog.Logger) {
	modelPull.mu.Lock()
	if modelPull.busy {
		modelPull.mu.Unlock()
		return
	}
	modelPull.busy = true
	modelPull.mu.Unlock()
	defer func() {
		modelPull.mu.Lock()
		modelPull.busy = false
		modelPull.mu.Unlock()
	}()

	want, err := FetchDesiredModel(ctx, "")
	if err != nil || want == "" || strings.EqualFold(want, current) {
		return
	}
	log.Info("model changed, downloading weights", "model", want)
	if err := OllamaPullWithProgress(ctx, want, nil); err != nil {
		log.Warn("model was not downloaded", "model", want, "err", err)
		return
	}
	cfg, err := workercfg.Load(cfgPath)
	if err != nil {
		log.Warn("cannot store new model", "err", err)
		return
	}
	cfg.Model = want
	if !strings.EqualFold(want, DefaultChatModel) {
		cfg.DraftGGUFPath = ""
	}
	if err := workercfg.Save(cfgPath, cfg); err != nil {
		log.Warn("cannot store new model", "err", err)
		return
	}
	log.Info("model is ready", "model", want)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// sleep ждёт указанный срок, возвращая false при отмене контекста.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// MaskToken показывает только префикс секрета — безопасно для логов и вывода.
func MaskToken(token string) string {
	if len(token) <= 14 {
		return "***"
	}
	return token[:14] + "…"
}
