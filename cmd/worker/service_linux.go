//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/private-compute/network/internal/worker"
	"github.com/private-compute/network/internal/workercfg"
)

const (
	linuxServiceName = "compute-node"
	linuxBinaryPath  = "/usr/local/bin/compute-node"
	linuxUnitPath    = "/etc/systemd/system/compute-node.service"
)

func cmdSetup(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	code := fs.String("code", "", "код сети, если владелец его включил")
	name := fs.String("name", "", "имя узла, по умолчанию имя компьютера")
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)
	requireRoot("setup")

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	logf := func(line string) { fmt.Println("  " + line) }
	progress := func(percent int, status string) {
		fmt.Printf("  [%3d%%] %s\n", percent, status)
	}

	fmt.Println("=== установка узла ===")
	if err := worker.EnsureOllama(ctx, worker.EnsureOllamaOptions{
		Ask:      func() bool { return true },
		Log:      logf,
		Progress: progress,
	}); err != nil {
		fail(err.Error())
	}
	if dir, err := worker.EnsureLlama(ctx, worker.EnsureLlamaOptions{Log: logf, Progress: progress}); err != nil {
		fail("llama.cpp: " + err.Error())
	} else {
		logf("llama.cpp: " + dir)
	}

	path := *cfgPath
	if path == "" {
		path = workercfg.DefaultPath()
	}
	cfg, err := workercfg.Load(path)
	if err != nil || cfg.DeviceToken == "" {
		fmt.Println("=== подключение к сети ===")
		result, err := worker.SelfRegister(ctx, worker.SelfRegisterOptions{
			JoinCode:   *code,
			Name:       *name,
			ConfigPath: path,
			PullModel:  true,
			AskOllama:  func() bool { return true },
			Log:        logf,
			Progress:   progress,
		})
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("узел подключён, модель %s\n", result.Model)
		if result.APIKey != "" {
			fmt.Printf("ключ API: %s\n", worker.MaskToken(result.APIKey))
		}
	} else {
		fmt.Println("узел уже зарегистрирован, повторно его не создаю")
		model := cfg.Model
		if model == "" {
			model = worker.ResolveChatModel("")
		}
		if err := worker.OllamaPull(ctx, model); err != nil {
			fail("не удалось скачать модель: " + err.Error())
		}
	}

	if err := installLinuxBinary(); err != nil {
		fail(err.Error())
	}
	if err := writeLinuxUnit(path); err != nil {
		fail(err.Error())
	}
	if err := systemctl("daemon-reload"); err != nil {
		fail(err.Error())
	}
	if err := systemctl("enable", "--now", linuxServiceName); err != nil {
		fail(err.Error())
	}
	fmt.Println("служба compute-node запущена")
	fmt.Println("остановка с освобождением видеокарты: sudo compute-node stop")
	_ = logger
}

func cmdStart(args []string, _ *slog.Logger) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)
	requireRoot("start")
	if !setPausedAt(*cfgPath, false) {
		return
	}
	if err := systemctl("start", linuxServiceName); err != nil {
		fail(err.Error())
	}
	fmt.Println("узел запущен и снова берёт задания")
	fmt.Println("модель встанет на видеокарту, когда придёт сообщение в чат")
}

func cmdStop(args []string, _ *slog.Logger) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	cfgPath := fs.String("config", "", "путь к конфигурации")
	_ = fs.Parse(args)
	requireRoot("stop")
	setPausedAt(*cfgPath, true)
	if err := systemctl("stop", linuxServiceName); err != nil {
		fmt.Println("служба: " + err.Error())
	}
	freed := worker.ReleaseLocalGPU()
	if freed > 0 {
		fmt.Printf("узел остановлен, с видеокарты снято процессов: %d\n", freed)
		return
	}
	fmt.Println("узел остановлен, видеокарта свободна")
}

func cmdUpdate(args []string, _ *slog.Logger) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	_ = fs.Parse(args)
	requireRoot("update")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := worker.InstallRelease(ctx, worker.Release{}, func(done, total int64) {
		if total > 0 {
			fmt.Printf("\rскачиваю обновление: %d%%", done*100/total)
		}
	}); err != nil {
		fmt.Println()
		fail(err.Error())
	}
	fmt.Println()
	fmt.Println("программа обновлена, служба перезапускается")
}

func requireRoot(command string) {
	if os.Geteuid() != 0 {
		fail(command + " запускается от root: sudo compute-node " + command)
	}
}

func setPausedAt(cfgPath string, paused bool) bool {
	cfg, err := workercfg.Load(cfgPath)
	if err != nil {
		fail(err.Error())
	}
	cfg.Paused = paused
	if err := workercfg.Save(cfgPath, cfg); err != nil {
		fail(err.Error())
	}
	return true
}

func installLinuxBinary() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if filepath.Clean(exe) == linuxBinaryPath {
		return nil
	}
	input, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(linuxBinaryPath), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(linuxBinaryPath+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(linuxBinaryPath+".new", linuxBinaryPath)
}

func writeLinuxUnit(cfgPath string) error {
	if cfgPath == "" {
		cfgPath = workercfg.DefaultPath()
	}
	body := fmt.Sprintf(`[Unit]
Description=Compute Network participant
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run --config %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, linuxBinaryPath, cfgPath)
	return os.WriteFile(linuxUnitPath, []byte(body), 0o644)
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		text := string(out)
		if len(text) > 300 {
			text = text[len(text)-300:]
		}
		return fmt.Errorf("systemctl %s: %w: %s", args[0], err, text)
	}
	return nil
}
