//go:build !windows && !linux

package worker

import (
	"context"
	"fmt"
)

func downloadOllamaSetup(context.Context, func(done, total int64)) (string, error) {
	return "", fmt.Errorf("скачивание установщика Ollama есть только в Windows-программе и на Linux")
}

func runOllamaSetup(string) error {
	return fmt.Errorf("установите Ollama с https://ollama.com/download")
}

func stopOllamaModelServers() int { return 0 }

func ReleaseLocalGPU() int { return 0 }
