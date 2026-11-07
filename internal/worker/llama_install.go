package worker

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Сборка, на которой на этой машине уже измерялся GPT-OSS. Новее не берём,
// пока нет своего замера.
const llamaRelease = "b12167"

// EnsureLlamaOptions — куда писать ход установки.
type EnsureLlamaOptions struct {
	Log      func(string)
	Progress func(percent int, status string)
}

var llamaInstallMu sync.Mutex

// EnsureLlama ставит llama-server и ggml-rpc-server, если их ещё нет.
//
// Готовый каталог не трогает: ни свою прошлую установку, ни ручную.
// Ошибка скачивания не должна ронять узел — чат без разделения слоёв
// по-прежнему идёт через Ollama.
func EnsureLlama(ctx context.Context, opts EnsureLlamaOptions) (string, error) {
	llamaInstallMu.Lock()
	defer llamaInstallMu.Unlock()
	if dir := findLlamaDir(); dir != "" {
		return dir, nil
	}
	return installLlama(ctx, opts)
}

var llamaInstallOnce sync.Once

// startLlamaInstall качает сборку в фоне, если её ещё нет на диске.
func startLlamaInstall(ctx context.Context, log *slog.Logger) {
	llamaInstallOnce.Do(func() {
		go func() {
			_, err := EnsureLlama(ctx, EnsureLlamaOptions{
				Log: func(line string) {
					if log != nil {
						log.Info(line)
					}
				},
			})
			if err != nil && log != nil {
				log.Warn("llama.cpp не установлена", "err", err)
			}
		}()
	})
}

// llamaInstallDir — каталог программы, не системный.
func llamaInstallDir() string {
	if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
		return filepath.Join(base, "ComputeNetwork", "llama")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".compute-network", "llama")
}

// llamaRuntimeReady — в каталоге есть оба файла, а на Windows ещё и CUDA.
func llamaRuntimeReady(dir string) bool {
	if !llamaDirComplete(dir) {
		return false
	}
	if filepath.Separator != '\\' {
		return true
	}
	matches, err := filepath.Glob(filepath.Join(dir, "cudart64_*.dll"))
	return err == nil && len(matches) > 0
}

// llamaCUDAFamily выбирает архив под версию CUDA из nvidia-smi.
// Пустая версия остаётся на 13.4: это сборка, которая уже запускалась здесь.
func llamaCUDAFamily(version string) string {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	n, err := strconv.Atoi(major)
	if err != nil || n >= 13 {
		return "13.4"
	}
	return "12.4"
}

// extractZip распаковывает архив в каталог и не даёт файлу выйти наружу.
func extractZip(src, dest string) error {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer reader.Close()

	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	for _, file := range reader.File {
		name := filepath.Clean(filepath.FromSlash(file.Name))
		if name == "." || name == "" {
			continue
		}
		target := filepath.Join(root, name)
		rel, err := filepath.Rel(root, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("плохой путь в архиве: %s", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
