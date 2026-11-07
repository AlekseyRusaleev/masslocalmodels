// Команда worker-agent — Worker Agent платформы.
//
// Режимы:
//
//	worker-agent enroll --token <одноразовый токен> [--name owner-pc] [--control URL]
//	worker-agent run [--once]
//	worker-agent status
//	worker-agent discover
//	worker-agent fetch --url <URL> --dest <path> [--sha256 hex] [--streams N]
//	worker-agent pause | resume
//	worker-agent version
//
// Агент инициирует только исходящие соединения и не слушает порты.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/private-compute/network/internal/download"
	"github.com/private-compute/network/internal/gguf"
	"github.com/private-compute/network/internal/worker"
	"github.com/private-compute/network/internal/workercfg"
)

const defaultControl = "https://control.nshno.tech"

// cliCommands — команды агента; всё остальное считаем графическим режимом.
var cliCommands = map[string]bool{
	"join": true, "register": true, "enroll": true, "run": true, "status": true, "discover": true,
	"fetch": true, "model": true, "benchmark": true, "gguf": true, "serve": true,
	"mesh": true, "mesh-link": true, "pause": true, "resume": true,
	"setup": true, "start": true, "stop": true, "update": true,
	"version": true, "--version": true, "-v": true,
}

// runCLI выполняет команду агента (тот же файл, что и окно с треем).
func runCLI(args []string) {
	command := "run"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch command {
	case "join":
		cmdJoin(args, logger)
	case "register":
		cmdRegister(args, logger)
	case "enroll":
		cmdEnroll(args, logger)
	case "run":
		cmdRun(args, logger)
	case "status":
		cmdStatus(args)
	case "discover":
		cmdDiscover(args, logger)
	case "fetch":
		cmdFetch(args, logger)
	case "model":
		cmdModel(args, logger)
	case "benchmark":
		cmdBenchmark(args, logger)
	case "gguf":
		cmdGGUF(args)
	case "serve":
		cmdServe(args, logger)
	case "mesh":
		cmdMesh(args, logger)
	case "mesh-link":
		cmdMeshLink(args, logger)
	case "pause":
		cmdSetPaused(args, true)
		worker.ReleaseLocalGPU()
	case "resume":
		cmdSetPaused(args, false)
	case "setup":
		cmdSetup(args, logger)
	case "start":
		cmdStart(args, logger)
	case "stop":
		cmdStop(args, logger)
	case "update":
		cmdUpdate(args, logger)
	case "version", "--version", "-v":
		fmt.Printf("worker-agent %s (%s/%s)\n", worker.AgentVersion, runtime.GOOS, runtime.GOARCH)
	default:
		fail("unknown command: " + command)
	}
}

// cmdJoin — подключение к сети по приглашению.
//
// Вся работа выполняется общим сценарием worker.Onboard: он же используется
// установщиком с интерфейсом, поэтому шаги не расходятся.
func cmdJoin(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	inviteArg := fs.String("invite", "", "код приглашения из панели или путь к файлу")
	name := fs.String("name", "", "имя узла (по умолчанию hostname)")
	cfgPath := fs.String("config", "", "путь к конфигурации")
	modelFlag := fs.String("model", "", "переопределить модель из приглашения")
	runBench := fs.Bool("bench", true, "измерить скорость и отправить калибровку")
	pull := fs.Bool("pull", false, "скачать модель в Ollama, если её нет")
	harnessPath := fs.String("harness-out", "", "куда записать настройки для харнеса")
	_ = fs.Parse(args)

	if *inviteArg == "" {
		fail("--invite обязателен: вставьте код из панели (Узлы → Подключить компьютер)")
	}
	invite, err := worker.ParseInvite(*inviteArg)
	if err != nil {
		fail(err.Error())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	model := invite.Model
	if *modelFlag != "" {
		model = *modelFlag
	}

	fmt.Println("=== подключение к сети ===")
	result, err := worker.Onboard(ctx, worker.OnboardOptions{
		Invite:      invite,
		Name:        *name,
		Model:       model,
		ConfigPath:  *cfgPath,
		HarnessPath: *harnessPath,
		PullModel:   *pull,
		Benchmark:   *runBench,
		Logger:      logger,
		Log: func(line string) {
			fmt.Println("  " + line)
		},
		Progress: func(percent int, status string) {
			fmt.Printf("  [%3d%%] %s\n", percent, status)
		},
	})
	if err != nil {
		fail(err.Error())
	}

	fmt.Println()
	fmt.Println("=== настройки для харнеса ===")
	fmt.Print(result.HarnessYAML)
	fmt.Println()
	fmt.Println("=== что осталось ===")
	fmt.Println("  1) автозапуск: compute-node.exe run --model " + result.Model + " (или install-worker-agent.ps1 -Invite <код>)")
	fmt.Println("  2) проверка:   compute-node.exe status")
	fmt.Println("  3) фрагмент выше — в settings.yaml харнеса, ключ — в .credentials.yaml")
}

// cmdRegister — самозапись узла без приглашения.
//
// Путь участника не должен зависеть от владельца сети: команда спрашивает у
// платформы секрет устройства, ключ для харнеса и модель, сохраняет всё локально
// и настраивает харнес. Код сети нужен только если владелец его включил.
func cmdRegister(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	control := fs.String("control", "", "адрес платформы (пусто — адрес по умолчанию)")
	code := fs.String("code", "", "код сети, если владелец его включил")
	name := fs.String("name", "", "имя узла (по умолчанию имя компьютера)")
	cfgPath := fs.String("config", "", "путь к конфигурации")
	pull := fs.Bool("pull", true, "скачать модель, если её нет локально")
	_ = fs.Parse(args)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Println("=== самозапись узла ===")
	result, err := worker.SelfRegister(ctx, worker.SelfRegisterOptions{
		ControlURL: *control,
		JoinCode:   *code,
		Name:       *name,
		ConfigPath: *cfgPath,
		PullModel:  *pull,
		Log: func(line string) {
			fmt.Println("  " + line)
		},
		Progress: func(percent int, status string) {
			fmt.Printf("  [%3d%%] %s\n", percent, status)
		},
	})
	if err != nil {
		fail(err.Error())
	}

	fmt.Println()
	fmt.Println("Узел подключён к сети.")
	fmt.Printf("  имя:        %s\n", result.Config.WorkerName)
	fmt.Printf("  control:    %s\n", result.Config.ControlURL)
	fmt.Printf("  конфиг:     %s\n", choosePath(*cfgPath))
	fmt.Printf("  модель:     %s\n", orNone(result.Model))
	fmt.Printf("  доверие:    %s\n", orNone(result.TrustClass))
	if result.HarnessFile != "" {
		fmt.Printf("  харнес:     %s\n", result.HarnessFile)
	}
	if result.APIKey != "" {
		fmt.Printf("  ключ API:   %s\n", worker.MaskToken(result.APIKey))
	}
	fmt.Println()
	fmt.Println("Дальше: автозапуск ставится командой install-worker-agent.ps1, запуск — compute-node.exe run")
}

func cmdEnroll(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	token := fs.String("token", "", "одноразовый токен подключения из панели")
	name := fs.String("name", "", "имя узла (по умолчанию hostname)")
	control := fs.String("control", defaultControl, "URL Control Plane")
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)

	if *token == "" {
		fail("--token is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, info, err := worker.Enroll(ctx, *cfgPath, *control, *token, *name, logger)
	if err != nil {
		fail(err.Error())
	}

	fmt.Println()
	fmt.Println("Узел зарегистрирован.")
	fmt.Printf("  имя:        %s\n", cfg.WorkerName)
	fmt.Printf("  control:    %s\n", cfg.ControlURL)
	fmt.Printf("  конфиг:     %s\n", choosePath(*cfgPath))
	fmt.Printf("  ОС:         %s\n", info.OS)
	fmt.Printf("  CPU:        %s (%d потоков)\n", info.CPUModel, info.CPUCores)
	fmt.Printf("  RAM:        %.1f GB\n", float64(info.RAMBytes)/1024/1024/1024)
	for _, gpu := range info.GPUs {
		fmt.Printf("  GPU:        %s, VRAM %.1f GB, драйвер %s, CUDA %s\n",
			gpu.Name, float64(gpu.VRAMBytes)/1024/1024/1024, gpu.DriverVer, info.CUDAVer)
	}
	if len(info.GPUs) == 0 {
		fmt.Println("  GPU:        не обнаружены (nvidia-smi недоступен)")
	}
	for _, disk := range info.Disks {
		fmt.Printf("  диск %-4s  свободно %.1f GB из %.1f GB\n",
			disk.Mount, float64(disk.FreeBytes)/1024/1024/1024, float64(disk.TotalBytes)/1024/1024/1024)
	}
	fmt.Println()
	fmt.Println("Секрет устройства сохранён только локально. Запуск: worker-agent run")
}

func cmdRun(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	once := fs.Bool("once", false, "отправить один heartbeat и выйти")
	model := fs.String("model", "", "модель для чат-заданий (по умолчанию из конфигурации)")
	numCtx := fs.Int("num-ctx", 0, "окно контекста в токенах (0 — по умолчанию 16384)")
	cfgPath := fs.String("config", "", "путь к конфигурации")
	logFile := fs.String("log", "", "дополнительно писать журнал в файл (с ротацией)")
	_ = fs.Parse(args)

	if *logFile != "" {
		logger = teeLogger(logger, *logFile)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Модель и окно контекста, заданные флагами, запоминаются в конфигурации:
	// перезапуск из автозапуска не требует повторять их в командной строке.
	if *numCtx > 0 || *model != "" {
		cfg, err := workercfg.Load(*cfgPath)
		if err != nil {
			fail(err.Error())
		}
		changed := false
		if *model != "" && cfg.Model != *model {
			cfg.Model = *model
			changed = true
		}
		if *numCtx > 0 && cfg.NumCtx != *numCtx {
			cfg.NumCtx = *numCtx
			changed = true
		}
		if changed {
			if err := workercfg.Save(*cfgPath, cfg); err != nil {
				fail(err.Error())
			}
		}
	}

	servedModel := ""
	if cfg, err := workercfg.Load(*cfgPath); err == nil {
		servedModel = cfg.Model
		// Координатор шард-группы обслуживает задания даже без локальной
		// модели в Ollama: его модель собрана из слоёв нескольких узлов.
		if meshModel := worker.MeshServedModel(cfg); meshModel != "" {
			servedModel = meshModel
		}
	}

	// Один процесс делает обе работы: heartbeat и выполнение заданий.
	if servedModel != "" && !*once {
		numCtx := worker.DefaultNumCtx
		if cfg, err := workercfg.Load(*cfgPath); err == nil {
			numCtx = worker.NumCtxOf(cfg)
		}
		go func() {
			logger.Info("serving chat jobs",
				"runtime", worker.OllamaEndpoint, "model", servedModel, "num_ctx", numCtx)
			if err := worker.ServeJobs(ctx, *cfgPath, servedModel, logger); err != nil {
				logger.Error("job loop stopped", "err", err)
			}
		}()
	}

	if err := worker.Run(ctx, *cfgPath, *once, logger); err != nil {
		fail(err.Error())
	}
}

// cmdDiscover печатает обнаруженное железо и телеметрию без обращения к серверу.
func cmdDiscover(_ []string, logger *slog.Logger) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	info, err := worker.Discover(ctx)
	if err != nil {
		fail(err.Error())
	}
	fmt.Println("=== hardware ===")
	fmt.Printf("hostname: %s\n", info.Hostname)
	fmt.Printf("os:       %s\n", info.OS)
	fmt.Printf("cpu:      %s (%d потоков)\n", info.CPUModel, info.CPUCores)
	fmt.Printf("ram:      %.1f GB\n", float64(info.RAMBytes)/1024/1024/1024)
	for _, gpu := range info.GPUs {
		fmt.Printf("gpu:      %s | vram %.1f GB | driver %s | compute %s\n",
			gpu.Name, float64(gpu.VRAMBytes)/1024/1024/1024, gpu.DriverVer, gpu.ComputeCap)
	}
	fmt.Printf("cuda:     %s\n", info.CUDAVer)
	for _, disk := range info.Disks {
		fmt.Printf("disk %-4s free %.1f GB / %.1f GB\n",
			disk.Mount, float64(disk.FreeBytes)/1024/1024/1024, float64(disk.TotalBytes)/1024/1024/1024)
	}

	tel, err := worker.Collect(ctx, info.Disks)
	fmt.Println("=== telemetry ===")
	if err != nil {
		logger.Warn("telemetry unavailable", "err", err)
		return
	}
	printOptional("cpu_percent", tel.CPUPercent)
	if tel.RAMFreeBytes != nil {
		fmt.Printf("ram_free:   %.1f GB\n", float64(*tel.RAMFreeBytes)/1024/1024/1024)
	}
	if tel.VRAMFreeBytes != nil {
		fmt.Printf("vram_free:  %.2f GB\n", float64(*tel.VRAMFreeBytes)/1024/1024/1024)
	}
	if tel.DiskFreeBytes != nil {
		fmt.Printf("disk_free:  %.1f GB\n", float64(*tel.DiskFreeBytes)/1024/1024/1024)
	}
	printOptional("gpu_temp_c", tel.TemperatureC)
	printOptional("power_w", tel.PowerWatts)
}

// printOptional печатает необязательное поле, если оно известно.
func printOptional(label string, value *float32) {
	if value != nil {
		fmt.Printf("%s: %.1f\n", label, float64(*value))
	}
}

// cmdFetch скачивает артефакт модели прямо из источника с проверкой хеша.
// Так вес идёт от источника к узлу, минуя координатор (план 5.3-5.5),
// а крупные файлы качаются несколькими диапазонами параллельно.
func cmdFetch(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	url := fs.String("url", "", "URL артефакта (Hugging Face, S3)")
	dest := fs.String("dest", "", "куда сохранить файл")
	sha := fs.String("sha256", "", "ожидаемый SHA-256 (hex); пусто — вычислить и напечатать")
	retries := fs.Int("retries", 3, "число попыток при обрыве")
	streams := fs.Int("streams", 8, "параллельных соединений для крупных файлов (1 — один поток)")
	_ = fs.Parse(args)

	if *url == "" || *dest == "" {
		fail("--url and --dest are required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	lastReport := time.Now()
	result, err := download.Fetch(ctx, download.Options{
		URL:         *url,
		DestPath:    *dest,
		ExpectedSHA: *sha,
		Retries:     *retries,
		Streams:     *streams,
		Progress: func(done, total int64) {
			if time.Since(lastReport) < 2*time.Second {
				return
			}
			lastReport = time.Now()
			if total > 0 {
				fmt.Printf("\r%d%%  %.0f / %.0f MB", done*100/total,
					float64(done)/1024/1024, float64(total)/1024/1024)
				return
			}
			fmt.Printf("\r%.0f MB", float64(done)/1024/1024)
		},
	})
	fmt.Println()
	if err != nil {
		fail(err.Error())
	}

	logger.Info("artifact fetched",
		"path", result.Path,
		"mb", int(float64(result.Bytes)/1024/1024),
		"resumed", result.Resumed,
		"seconds", int(result.Elapsed.Seconds()),
		"avg_mbps", int(float64(result.AvgBytes)/1024/1024),
	)
	fmt.Printf("файл:    %s\n", result.Path)
	fmt.Printf("размер:  %.1f MB\n", float64(result.Bytes)/1024/1024)
	fmt.Printf("sha256:  %s\n", result.SHA256)
	if *sha == "" {
		fmt.Println("хеш не сверялся: закрепите его в манифесте модели и передавайте через --sha256")
	}
}

// cmdModel скачивает ревизию модели по манифесту Control Plane.
// Узел забирает файлы напрямую из источника и сверяет SHA-256 (ADR 0008 §5.4).
func cmdModel(args []string, logger *slog.Logger) {
	if len(args) == 0 || args[0] != "fetch" {
		fail("usage: worker-agent model fetch --revision <id> [--dir <path>] [--streams N]")
	}

	fs := flag.NewFlagSet("model fetch", flag.ExitOnError)
	revisionID := fs.String("revision", "", "ID ревизии из каталога моделей")
	dir := fs.String("dir", "", "каталог кэша моделей (по умолчанию C:\\ComputeNetwork\\models или /var/lib/compute-network/models)")
	streams := fs.Int("streams", 8, "параллельных соединений на файл")
	force := fs.Bool("force", false, "скачать заново, даже если файл уже есть с верным хешем")
	cfgPath := fs.String("config", "", "путь к конфигурации агента")
	_ = fs.Parse(args[1:])

	if *revisionID == "" {
		fail("--revision is required")
	}

	cfg, err := workercfg.Load(*cfgPath)
	if err != nil {
		fail(err.Error())
	}
	client := worker.NewClient(cfg.ControlURL, cfg.DeviceToken)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	manifest, err := client.Manifest(ctx, *revisionID)
	if err != nil {
		fail(err.Error())
	}

	targetDir := *dir
	if targetDir == "" {
		targetDir = filepath.Join(workercfg.DefaultModelDir(), *revisionID)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		fail("cannot create model dir: " + err.Error())
	}

	logger.Info("manifest received",
		"model", manifest.Revision.ModelName,
		"revision", manifest.Revision.RevisionRef,
		"artifacts", len(manifest.Artifacts),
		"license", manifest.Revision.License,
	)

	var totalBytes int64
	for _, artifact := range manifest.Artifacts {
		dest := filepath.Join(targetDir, artifact.Name)

		if !*force && artifact.SHA256 != "" {
			if existing, err := download.FileSHA256(dest); err == nil && strings.EqualFold(existing, artifact.SHA256) {
				fmt.Printf("%-40s уже на диске, хеш совпал\n", artifact.Name)
				totalBytes += artifact.Size
				continue
			}
		}

		fmt.Printf("%-40s %.0f MB\n", artifact.Name, float64(artifact.Size)/1024/1024)
		result, err := download.Fetch(ctx, download.Options{
			URL:         artifact.URL,
			DestPath:    dest,
			ExpectedSHA: artifact.SHA256,
			Streams:     *streams,
			Retries:     3,
		})
		if err != nil {
			fail(fmt.Sprintf("download %s: %v", artifact.Name, err))
		}
		logger.Info("artifact ready",
			"name", artifact.Name,
			"mb", int(float64(result.Bytes)/1024/1024),
			"seconds", int(result.Elapsed.Seconds()),
			"avg_mbps", int(float64(result.AvgBytes)/1024/1024),
		)
		totalBytes += result.Bytes
	}

	fmt.Println()
	fmt.Printf("ревизия:  %s (%s)\n", manifest.Revision.ModelName, manifest.Revision.RevisionRef)
	fmt.Printf("каталог:  %s\n", targetDir)
	fmt.Printf("файлов:   %d, суммарно %.1f GB\n", len(manifest.Artifacts), float64(totalBytes)/1024/1024/1024)
}

// cmdBenchmark прогоняет калибровочный замер на локальном рантайме и сообщает
// результат Control Plane: Scheduler использует его вместо общей константы.
func cmdBenchmark(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
	model := fs.String("model", "", "локальная модель для замера (в рантайме узла)")
	billions := fs.Float64("params-billions", 8.2, "размер модели в миллиардах параметров")
	quantization := fs.String("quantization", "Q4_K_M", "квантование модели")
	tokens := fs.Int("tokens", 96, "сколько токенов сгенерировать для замера")
	report := fs.Bool("report", true, "отправить результат на Control Plane")
	cfgPath := fs.String("config", "", "путь к конфигурации агента")
	_ = fs.Parse(args)
	if strings.TrimSpace(*model) == "" || worker.IsRetiredModel(*model) {
		fail("укажите --model: тестовая qwen3:8b снята")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("benchmark started", "model", *model, "tokens", *tokens)
	result, err := worker.BenchViaOllama(ctx, *model, *tokens)
	if err != nil {
		fail(err.Error())
	}
	result.ModelBillions = *billions
	result.Quantization = *quantization

	msPerBillion := (1000 / result.TokensPerSec) / *billions

	fmt.Printf("модель:      %s (%.1fB, %s)\n", result.Model, *billions, *quantization)
	fmt.Printf("токенов:     %d за %.1f c\n", result.GeneratedToken, result.WallSeconds)
	fmt.Printf("скорость:    %.2f tok/s\n", result.TokensPerSec)
	fmt.Printf("на 1B:       %.3f мс\n", msPerBillion)
	fmt.Printf("загрузка:    %.0f мс\n", result.LoadMS)

	if !*report {
		fmt.Println("результат не отправлен (--report=false)")
		return
	}

	cfg, err := workercfg.Load(*cfgPath)
	if err != nil {
		fail(err.Error())
	}
	if err := worker.NewClient(cfg.ControlURL, cfg.DeviceToken).ReportBenchmark(ctx, result); err != nil {
		fail(err.Error())
	}
	logger.Info("benchmark reported", "worker", cfg.WorkerName, "tok_per_s", result.TokensPerSec)
	fmt.Println("результат отправлен на Control Plane: прогноз скорости для этого узла теперь калиброван")
}

// cmdGGUF показывает карту слоёв GGUF-файла и предлагает разбиение по частям.
func cmdGGUF(args []string) {
	fs := flag.NewFlagSet("gguf", flag.ExitOnError)
	file := fs.String("file", "", "путь к GGUF-файлу")
	parts := fs.Int("parts", 2, "на сколько частей предложить разбиение")
	jsonOut := fs.Bool("json", false, "вывести JSON вместо таблицы")
	names := fs.Bool("names", false, "показать имена тензоров по слоям")
	_ = fs.Parse(args)

	if *file == "" {
		fail("--file is required")
	}

	info, err := gguf.ReadInfo(*file)
	if err != nil {
		fail(err.Error())
	}

	if *jsonOut {
		payload := map[string]any{
			"file":          *file,
			"version":       info.Version,
			"architecture":  info.Architecture,
			"tensors":       info.TensorCount,
			"layers":        info.Layers,
			"shared":        info.SharedTensors,
			"tensors_bytes": info.TensorsBytes,
			"header_bytes":  info.HeaderBytes,
			"split":         gguf.SuggestSplit(info, *parts),
		}
		data, _ := jsonMarshal(payload)
		fmt.Println(string(data))
		return
	}

	fmt.Printf("файл:        %s\n", *file)
	fmt.Printf("архитектура: %s (GGUF v%d)\n", info.Architecture, info.Version)
	fmt.Printf("тензоров:    %d, слоёв: %d\n", info.TensorCount, len(info.Layers))
	fmt.Printf("заголовок:   %.2f MB, данные с %d\n", float64(info.HeaderBytes)/1024/1024, info.DataOffset)
	fmt.Printf("вес тензоров: %.2f GB\n", float64(info.TensorsBytes)/1024/1024/1024)

	fmt.Println()
	fmt.Println("служебные тензоры (нужны всем узлам):")
	for _, tensor := range info.SharedTensors {
		fmt.Printf("  %-28s %8.2f MB  тип %d\n", tensor.Name, float64(tensor.Bytes)/1024/1024, tensor.Type)
	}

	fmt.Println()
	fmt.Println("предлагаемое разбиение:")
	for _, part := range gguf.SuggestSplit(info, *parts) {
		fmt.Printf("  часть %d: слои %2d–%-2d, %6.2f GB\n",
			part.Part, part.LayerStart, part.LayerEnd, float64(part.Bytes)/1024/1024/1024)
	}

	if *names {
		fmt.Println()
		fmt.Println("слои:")
		for _, layer := range info.Layers {
			fmt.Printf("  слой %-2d: %2d тензоров, %6.2f MB\n",
				layer.Layer, layer.Tensors, float64(layer.Bytes)/1024/1024)
			for _, name := range layer.Names {
				fmt.Printf("      %s\n", name)
			}
		}
	}
}

// cmdServe — только цикл заданий, без heartbeat. Нужен для отладки;
// в обычной работе достаточно «worker-agent run --model ...».
func cmdServe(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	model := fs.String("model", "", "модель локального рантайма (по умолчанию из конфигурации)")
	cfgPath := fs.String("config", "", "путь к конфигурации агента")
	_ = fs.Parse(args)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("worker serving jobs", "runtime", worker.OllamaEndpoint, "model", *model)
	if err := worker.ServeJobs(ctx, *cfgPath, *model, logger); err != nil {
		fail(err.Error())
	}
}

// cmdMeshLink проверяет туннель отдельно от llama.cpp.
//
// Роль "listen" держит сторону стадии и перекладывает поток в указанный
// локальный адрес; роль "dial" поднимает локальный порт, через который
// туннель видно снаружи. Нужно для разбора: если байты не идут, видно,
// на какой стороне обрыв, без рантайма модели.
func cmdMeshLink(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("mesh-link", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации агента")
	role := fs.String("role", "", "роль: dial или listen")
	topology := fs.String("topology", "", "идентификатор топологии")
	stage := fs.String("stage", "", "идентификатор узла-стадии")
	target := fs.String("target", "127.0.0.1:50052", "локальный адрес RPC-сервера (для listen)")
	self := fs.String("self", "", "идентификатор этого узла (по умолчанию из конфигурации — не нужен для listen)")
	peer := fs.String("peer", "", "идентификатор второго участника (координатор для listen, стадия для dial)")
	printAddr := fs.Bool("print-addr", false, "напечатать локальный адрес туннеля и ждать")
	_ = fs.Parse(args)

	if *topology == "" || *stage == "" {
		fail("--topology и --stage обязательны")
	}
	client, err := worker.NewClientForConfig(*cfgPath)
	if err != nil {
		fail(err.Error())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *stage == "" {
		fail("укажите --stage: это идентификатор узла-стадии")
	}
	if *role == "listen" && *peer == "" {
		fail("для listen нужен --peer: идентификатор координатора")
	}
	if *role == "dial" && *self == "" {
		fail("для dial нужен --self: идентификатор этого узла (координатора)")
	}

	switch *role {
	case "listen":
		listener, err := worker.StartMeshTunnelListener(ctx, client, *topology, *stage, *peer, *target)
		if err != nil {
			fail(err.Error())
		}
		defer listener.Close()
		fmt.Printf("слушаю сторону стадии: topology=%s stage=%s -> %s\n", *topology, *stage, *target)
		<-ctx.Done()
	case "dial":
		dialer, err := worker.StartMeshTunnelDialer(ctx, client, *topology, *stage, *self)
		if err != nil {
			fail(err.Error())
		}
		defer dialer.Close()
		fmt.Printf("локальный адрес туннеля: %s\n", dialer.Addr())
		if *printAddr {
			<-ctx.Done()
		}
	default:
		fail("роль должна быть dial или listen")
	}
}

// cmdMesh — стадия шард-группы (ADR 0008).
//
// Роль "compute" поднимает только ggml-rpc-server: слои и активации без API.
// Роль "coordinator" поднимает llama-server, который собирает слои своей карты
// и RPC-стадий в одну модель и отдаёт обычный OpenAI-совместимый API.
//
// Флаги перекрывают конфигурацию: так стадию можно проверить, не трогая
// agent.json рабочего узла.
func cmdMesh(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("mesh", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации агента")
	role := fs.String("role", "", "роль: coordinator или compute (по умолчанию из конфигурации)")
	model := fs.String("model", "", "имя шард-модели")
	gguf := fs.String("gguf", "", "файл весов GGUF (нужен координатору)")
	llamaDir := fs.String("llama-dir", "", "каталог с llama-server и ggml-rpc-server")
	layers := fs.String("layers", "", "диапазон слоёв стадии, например 0-17")
	peers := fs.String("peers", "", "RPC-адреса соисполнителей через запятую (host:port)")
	split := fs.String("split", "", "доли модели по устройствам через запятую (--tensor-split)")
	ctxTokens := fs.Int("ctx", 0, "окно контекста шард-группы")
	serverPort := fs.Int("port", 0, "порт llama-server (по умолчанию 18081)")
	rpcPort := fs.Int("rpc-port", 0, "порт ggml-rpc-server (по умолчанию 50052)")
	threads := fs.Int("threads", 0, "потоки CPU-стадии")
	listen := fs.String("listen", "", "интерфейс RPC-сервера (по умолчанию 127.0.0.1)")
	serve := fs.Bool("serve", false, "после запуска обслуживать задания платформы")
	_ = fs.Parse(args)

	mesh := workercfg.Mesh{}
	if cfg, err := workercfg.Load(*cfgPath); err == nil && cfg.Mesh != nil {
		mesh = *cfg.Mesh
	}
	if *role != "" {
		mesh.Role = *role
	}
	if *model != "" {
		mesh.Model = *model
	}
	if *gguf != "" {
		mesh.GGUFPath = *gguf
	}
	if *llamaDir != "" {
		mesh.LlamaDir = *llamaDir
	}
	if *layers != "" {
		mesh.Layers = *layers
	}
	if *peers != "" {
		mesh.Peers = splitList(*peers)
	}
	if *split != "" {
		mesh.Split = parseShares(*split, logger)
	}
	if *ctxTokens > 0 {
		mesh.Ctx = *ctxTokens
	}
	if *serverPort > 0 {
		mesh.ServerPort = *serverPort
	}
	if *rpcPort > 0 {
		mesh.RPCPort = *rpcPort
	}
	if *threads > 0 {
		mesh.Threads = *threads
	}
	if *listen != "" {
		mesh.ListenHost = *listen
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	defer worker.StopMeshRuntime()

	mesh = worker.MeshNormalize(mesh)
	if mesh.Role == "" {
		fail("роль не задана: укажите --role coordinator или --role compute")
	}

	linkClient, linkErr := worker.NewClientForConfig(*cfgPath)
	if linkErr != nil {
		logger.Warn("mesh: no control plane client", "err", linkErr)
	}
	if _, err := worker.EnsureMeshRuntime(ctx, linkClient, mesh, logger); err != nil {
		fail(err.Error())
	}
	status := worker.MeshStatus()
	fmt.Printf("роль:        %s\n", status.Role)
	fmt.Printf("модель:      %s\n", orNone(status.Model))
	fmt.Printf("слои:        %s\n", orNone(status.Layers))
	fmt.Printf("адрес:       %s\n", status.Endpoint)
	if status.Digest != "" {
		fmt.Printf("digest весов: %s\n", status.Digest)
	}

	if !*serve {
		fmt.Println("стадия работает; Ctrl+C — остановить")
		<-ctx.Done()
		return
	}
	logger.Info("serving jobs over the shard group", "model", status.Model)
	if err := worker.ServeJobs(ctx, *cfgPath, status.Model, logger); err != nil {
		fail(err.Error())
	}
}

// splitList разбирает список через запятую, отбрасывая пустые элементы.
func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseShares разбирает доли раскладки по устройствам.
func parseShares(value string, logger *slog.Logger) []float32 {
	parts := splitList(value)
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		var share float32
		if _, err := fmt.Sscanf(part, "%g", &share); err != nil {
			logger.Warn("cannot parse tensor split share", "value", part, "err", err)
			continue
		}
		out = append(out, share)
	}
	return out
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)
	path := choosePath(*cfgPath)
	cfg, err := workercfg.Load(*cfgPath)
	if err != nil {
		fmt.Printf("конфиг: %s\n", path)
		fail(err.Error())
	}

	state := "available"
	if cfg.Paused {
		state = "paused"
	}

	fmt.Printf("конфиг:      %s\n", path)
	fmt.Printf("узел:        %s\n", cfg.WorkerName)
	fmt.Printf("control:     %s\n", cfg.ControlURL)
	fmt.Printf("состояние:   %s\n", state)
	fmt.Printf("модель:      %s\n", orNone(cfg.Model))
	fmt.Printf("окно:        %d токенов\n", worker.NumCtxOf(cfg))
	fmt.Printf("интервал:    %d s\n", cfg.HeartbeatSeconds)
	if mesh := worker.MeshOptionsOf(cfg); mesh != nil {
		fmt.Printf("шард:        %s, слои %s\n", mesh.Role, orNone(mesh.Layers))
		if len(mesh.Peers) > 0 {
			fmt.Printf("соисполнители: %d (%s)\n", len(mesh.Peers), strings.Join(mesh.Peers, ", "))
		}
		if mesh.GGUFPath != "" {
			fmt.Printf("веса:        %s\n", mesh.GGUFPath)
		}
		if status := worker.MeshStatus(); status.Running {
			fmt.Printf("шард-группа: работает, %s\n", status.Endpoint)
		}
	}
	fmt.Printf("токен:       %s\n", worker.MaskToken(cfg.DeviceToken))
	if !cfg.EnrolledAt.IsZero() {
		fmt.Printf("зарегистрирован: %s\n", cfg.EnrolledAt.Format(time.RFC3339))
	}
}

func cmdSetPaused(args []string, paused bool) {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)

	cfg, err := workercfg.Load(*cfgPath)
	if err != nil {
		fail(err.Error())
	}
	cfg.Paused = paused
	if err := workercfg.Save(*cfgPath, cfg); err != nil {
		fail(err.Error())
	}
	if paused {
		fmt.Println("агент поставлен на паузу: новые задания не запрашиваются")
		return
	}
	fmt.Println("агент возобновлён")
}

func choosePath(cfgPath string) string {
	if cfgPath != "" {
		return cfgPath
	}
	return workercfg.DefaultPath()
}

// teeLogger дублирует журнал в файл: консоль остаётся, но при работе из
// планировщика историю можно посмотреть после перезапуска.
func teeLogger(base *slog.Logger, path string) *slog.Logger {
	file, err := worker.OpenRotatingFile(path, 10<<20, 3)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: собственный журнал недоступен (%v), пишу только в консоль\n", err)
		return base
	}
	sink := io.MultiWriter(os.Stdout, file)
	return slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// orNone печатает прочерк вместо пустой строки: пустое поле в выводе
// не должно выглядеть как забытое значение.
func orNone(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(1)
}

// jsonMarshal — тонкая обёртка, чтобы не тянуть ошибку в каждый вызов печати.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
