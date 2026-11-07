//go:build !windows

package worker

import "os/exec"

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
