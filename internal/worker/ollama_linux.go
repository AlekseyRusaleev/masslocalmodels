//go:build linux

package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/private-compute/network/internal/download"
)

// Тот же выпуск Ollama, что уже стоит на машине владельца (0.34.2).
// Берём файл выпуска, а не «последний» скрипт с сайта.
const ollamaLinuxSetupURL = "https://github.com/ollama/ollama/releases/download/v0.34.2/install.sh"

func downloadOllamaSetup(ctx context.Context, progress func(done, total int64)) (string, error) {
	dir := filepath.Join(os.TempDir(), "compute-network")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "ollama-install-0.34.2.sh")
	if _, err := download.Fetch(ctx, download.Options{
		URL:      ollamaLinuxSetupURL,
		DestPath: dest,
		Retries:  3,
		Progress: progress,
	}); err != nil {
		return "", err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return "", err
	}
	return dest, nil
}

func runOllamaSetup(setup string) error {
	cmd := exec.Command("sh", setup)
	cmd.Env = append(os.Environ(), "OLLAMA_VERSION=0.34.2")
	if os.Geteuid() != 0 {
		cmd = exec.Command("sudo", "-E", "sh", setup)
		cmd.Env = append(os.Environ(), "OLLAMA_VERSION=0.34.2")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		if len(text) > 400 {
			text = text[len(text)-400:]
		}
		return fmt.Errorf("%w: %s", err, text)
	}
	return nil
}
