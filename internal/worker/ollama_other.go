//go:build !windows

package worker

import (
	"context"
	"fmt"
	"os/exec"
)

func OllamaExecutable() string {
	path, err := exec.LookPath("ollama")
	if err != nil {
		return ""
	}
	return path
}

func startOllama(exe string) error {
	cmd := exec.Command(exe, "serve")
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func stopOllamaModelServers() int { return 0 }

func ReleaseLocalGPU() int { return 0 }

func downloadOllamaSetup(context.Context, func(done, total int64)) (string, error) {
	return "", fmt.Errorf("скачивание установщика Ollama есть только в Windows-программе")
}

func runOllamaSetup(string) error {
	return fmt.Errorf("установите Ollama с https://ollama.com/download")
}
