//go:build windows

package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/private-compute/network/internal/download"
)

func OllamaExecutable() string {
	candidates := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Ollama", "ollama.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Ollama", "ollama.exe"),
		`C:\Program Files\Ollama\ollama.exe`,
	}
	if path, err := exec.LookPath("ollama"); err == nil {
		candidates = append([]string{path}, candidates...)
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func startOllama(exe string) error {
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func downloadOllamaSetup(ctx context.Context, progress func(done, total int64)) (string, error) {
	dest := filepath.Join(os.TempDir(), "OllamaSetup.exe")
	_, err := download.Fetch(ctx, download.Options{
		URL:      OllamaSetupURL,
		DestPath: dest,
		Streams:  4,
		Retries:  3,
		Progress: progress,
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

func runOllamaSetup(setup string) error {
	// Окно установщика не прячем: нужен его собственный запрос UAC.
	cmd := exec.Command(setup, "/SILENT", "/NORESTART")
	if out, err := cmd.CombinedOutput(); err != nil {
		text := string(out)
		if len(text) > 400 {
			text = text[:400]
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}

// stopOllamaModelServers останавливает llama-server, который Ollama держит
// ради уже загруженной модели. Пока этот процесс жив, запрос на выгрузку
// часто не возвращается, и карта так и остаётся занятой.
func stopOllamaModelServers() int {
	cmd := quiet(exec.Command("powershell.exe", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -like '*\Ollama\lib\ollama\llama-server*.exe' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue; $_.ProcessId }`))
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// ReleaseLocalGPU снимает с видеокарты модель, которую держит эта программа:
// llama-server Ollama и процессы группы слоёв из нашей сборки llama.cpp.
// Возвращает, сколько процессов остановлено.
func ReleaseLocalGPU() int {
	cmd := quiet(exec.Command("powershell.exe", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath } | ForEach-Object { '{0}|{1}' -f $_.ProcessId, $_.ExecutablePath }`))
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		pid, path, ok := strings.Cut(line, "|")
		if !ok || !holdsGPU(path) {
			continue
		}
		kill := quiet(exec.Command("taskkill.exe", "/PID", pid, "/F"))
		if kill.Run() == nil {
			n++
		}
	}
	return n
}
