package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/private-compute/network/internal/workercfg"
)

// OnboardOptions — параметры подключения узла к сети.
//
// Один и тот же сценарий используется консольной командой `join` и установщиком
// с интерфейсом: логика не должна расходиться между ними.
type OnboardOptions struct {
	Invite Invite
	Name   string
	Model  string
	// NumCtx — окно контекста узла. 0 означает значение по умолчанию:
	// участник не должен думать об этом числе, но опытный может задать своё.
	NumCtx      int
	ConfigPath  string
	HarnessPath string
	PullModel   bool
	AskOllama   func() bool
	Benchmark   bool
	Logger      *slog.Logger

	// Log получает строки журнала для показа пользователю.
	Log func(string)
	// Progress получает проценты и короткий статус.
	Progress func(percent int, status string)
}

// OnboardResult — итог подключения.
type OnboardResult struct {
	Config      workercfg.Config
	Model       string
	Hardware    SystemInfo
	ModelReady  bool
	Benchmark   *BenchResult
	HarnessYAML string
	HarnessFile string
	// HarnessInstalled — настройки вставлены в конфигурацию харнеса.
	HarnessInstalled bool
	// HarnessNote объясняет, что осталось сделать вручную.
	HarnessNote string
}

// Onboard выполняет весь путь подключения узла:
// железо → рантайм и модель → регистрация → калибровка → настройки харнеса.
func Onboard(ctx context.Context, opts OnboardOptions) (OnboardResult, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(fmt.Sprintf(format, args...))
		}
	}
	progress := func(percent int, status string) {
		if opts.Progress != nil {
			opts.Progress(percent, status)
		}
	}

	model := opts.Model
	if model == "" {
		model = opts.Invite.Model
	}
	if model == "" {
		if picked, err := FetchServedModel(ctx, opts.Invite.ControlURL, opts.Invite.APIKey); err == nil && picked != "" && !IsRetiredModel(picked) {
			model = picked
			logf("модель выбрана платформой: %s", model)
		}
	}
	model = ResolveChatModel(model)
	logf("модель для этого узла: %s", model)

	var result OnboardResult
	result.Model = model

	// --- железо ---
	progress(2, "проверяю железо")
	info, err := Discover(ctx)
	if err != nil {
		return result, fmt.Errorf("не удалось определить конфигурацию: %w", err)
	}
	result.Hardware = info
	logf("хост: %s (%s)", info.Hostname, info.OS)
	logf("CPU: %s, %d потоков", info.CPUModel, info.CPUCores)
	logf("RAM: %.1f GB", float64(info.RAMBytes)/1024/1024/1024)
	if len(info.GPUs) == 0 {
		return result, fmt.Errorf("видеокарта NVIDIA не найдена: без неё узел не сможет считать модель")
	}
	for _, gpu := range info.GPUs {
		logf("GPU: %s, VRAM %.1f GB, драйвер %s",
			gpu.Name, float64(gpu.VRAMBytes)/1024/1024/1024, gpu.DriverVer)
		if gpu.VRAMBytes < 7*1024*1024*1024 {
			logf("внимание: меньше 8 GB VRAM, модель может не поместиться целиком")
		}
	}
	progress(10, "железо в порядке")

	hasModel := false
	if model != "" {
		progress(12, "проверяю Ollama")
		if err := EnsureOllama(ctx, EnsureOllamaOptions{
			Ask:      opts.AskOllama,
			Log:      opts.Log,
			Progress: opts.Progress,
		}); err != nil {
			if errors.Is(err, ErrOllamaDeclined) {
				return result, fmt.Errorf("без Ollama модель не скачается: установка отклонена")
			}
			return result, err
		}
		progress(11, "проверяю llama.cpp")
		if dir, err := EnsureLlama(ctx, EnsureLlamaOptions{Log: opts.Log, Progress: opts.Progress}); err != nil {
			logf("llama.cpp не установлена, разделение слоёв недоступно: %v", err)
		} else {
			logf("llama.cpp: %s", dir)
		}
		models, err := OllamaModels(ctx)
		if err != nil {
			return result, fmt.Errorf("Ollama недоступна на 127.0.0.1:11434: %w", err)
		}
		logf("Ollama отвечает, моделей локально: %d", len(models))

		for _, name := range models {
			if name == model {
				hasModel = true
			}
		}

		if !hasModel {
			if !opts.PullModel {
				return result, fmt.Errorf("модели %s нет в Ollama; запустите с загрузкой или выполните ollama pull %s", model, model)
			}
			logf("скачиваю модель %s", model)
			progress(18, "скачиваю модель")
			err := OllamaPullWithProgress(ctx, model, func(p PullProgress) {
				switch {
				case p.Total > 0:
					share := float64(p.Completed) / float64(p.Total)
					progress(18+int(share*42), fmt.Sprintf("скачиваю модель: %d%%", int(share*100)))
				case p.Status != "":
					progress(18, "скачиваю модель: "+p.Status)
				}
			})
			if err != nil {
				return result, fmt.Errorf("не удалось скачать модель: %w", err)
			}
			logf("модель %s загружена", model)
			hasModel = true
		}
		result.ModelReady = hasModel
		if gptOSS20(model) {
			progress(0, "скачиваю голову черновика")
			logf("скачиваю голову черновика")
			if draft, err := FetchDraftHead(ctx, func(done, total int64) {
				if total <= 0 {
					return
				}
				pct := done * 100 / total
				if pct > 99 {
					pct = 99
				}
				progress(int(pct), fmt.Sprintf("голова черновика: %d%%", pct))
			}); err != nil {
				logf("голова черновика не скачана: %v", err)
			} else {
				logf("голова черновика: %s", draft)
				result.Config.DraftGGUFPath = draft
			}
		}
		progress(60, "модель готова")
	} else {
		progress(60, "доля весов придёт с платформы")
	}

	// --- регистрация ---
	progress(62, "регистрирую узел")
	logf("регистрирую узел на %s", opts.Invite.ControlURL)
	cfg, _, err := Enroll(ctx, opts.ConfigPath, opts.Invite.ControlURL, opts.Invite.EnrollmentToken, opts.Name, logger)
	if err != nil {
		return result, fmt.Errorf("регистрация не выполнена: %w", err)
	}
	cfg.Model = model
	if result.Config.DraftGGUFPath != "" {
		cfg.DraftGGUFPath = result.Config.DraftGGUFPath
	}
	if opts.NumCtx > 0 {
		cfg.NumCtx = opts.NumCtx
	}
	if cfg.NodeKey == "" {
		cfg.NodeKey = nodeKey()
	}
	if err := workercfg.Save(opts.ConfigPath, cfg); err != nil {
		return result, err
	}
	result.Config = cfg
	logf("узел %s зарегистрирован на %s", cfg.WorkerName, cfg.ControlURL)
	logf("секрет устройства: %s", workercfg.SecretProtection())
	progress(70, "узел зарегистрирован")

	// --- калибровка ---
	if opts.Benchmark && hasModel {
		progress(72, "измеряю скорость")
		bench, err := BenchViaOllama(ctx, model, 96)
		if err != nil {
			logf("замер не выполнен: %v", err)
		} else {
			bench.ModelBillions = 21
			bench.Quantization = "MXFP4"
			result.Benchmark = &bench
			logf("скорость: %.2f tok/s (%d токенов за %.1f c)",
				bench.TokensPerSec, bench.GeneratedToken, bench.WallSeconds)
			if err := NewClient(cfg.ControlURL, cfg.DeviceToken).ReportBenchmark(ctx, bench); err != nil {
				logf("калибровка не отправлена: %v", err)
			} else {
				logf("калибровка отправлена: платформа знает скорость этого узла")
			}
		}
	}
	progress(90, "калибровка готова")

	// --- настройки харнеса ---
	harness := opts.Invite.HarnessConfig(model)
	result.HarnessYAML = harness
	if opts.HarnessPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.HarnessPath), 0o755); err != nil {
			logf("не удалось создать каталог для настроек: %v", err)
		} else if err := os.WriteFile(opts.HarnessPath, []byte(harness), 0o644); err != nil {
			logf("не удалось записать %s: %v", opts.HarnessPath, err)
		} else {
			result.HarnessFile = opts.HarnessPath
			logf("настройки харнеса записаны: %s", opts.HarnessPath)
		}
	}
	// Пробуем сразу вставить настройки в харнес: если он на этой машине есть,
	// участнику не нужно ничего копировать вручную.
	if model != "" {
		if install, err := InstallHarnessSettings(opts.Invite.APIKey, model, opts.Invite.ControlURL); err != nil {
			logf("настройки харнеса не вставлены автоматически: %v", err)
			result.HarnessNote = err.Error()
		} else {
			result.HarnessInstalled = install.SettingsChanged || install.CredentialsChanged
			result.HarnessNote = install.Note
			switch {
			case result.HarnessInstalled:
				logf("настройки вставлены в харнес: %s", install.SettingsPath)
				if len(install.Backups) > 0 {
					logf("резервные копии: %d", len(install.Backups))
				}
			case install.Note != "":
				logf("харнес: %s", install.Note)
			}
		}
	}

	progress(100, "готово")
	return result, nil
}

// nodeKey — короткий идентификатор узла для человека.
func nodeKey() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// DefaultTaskName — имя задачи автозапуска по умолчанию.
const DefaultTaskName = "ComputeNetwork Worker Agent"

// RunAutostart регистрирует задачу планировщика для текущего пользователя.
//
// Права администратора не нужны: задача создаётся в профиле пользователя и
// запускается при входе. Окно консоли скрыто. Пустое имя задачи означает
// DefaultTaskName; своё имя нужно, когда на одной машине несколько узлов.
func RunAutostart(execPath, configPath, model, taskName string) error {
	if execPath == "" || configPath == "" {
		return fmt.Errorf("нужен путь к агенту и к конфигурации")
	}
	if taskName == "" {
		taskName = DefaultTaskName
	}
	arguments := strings.Join([]string{
		"run",
		"--model", model,
		"--config", configPath,
		"--log", filepath.Join(filepath.Dir(configPath), "logs", "worker-agent.log"),
	}, " ")

	return registerLogonTask(taskName, execPath, arguments)
}
