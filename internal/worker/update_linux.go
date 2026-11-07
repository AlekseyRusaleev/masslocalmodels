//go:build linux

package worker

import (
	"fmt"
	"os"
	"os/exec"
)

func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("не удалось определить путь программы: %w", err)
	}
	return exe, nil
}

// swapExecutable подменяет файл. На Linux работающий процесс держит старый
// inode, поэтому новый файл можно положить на то же имя сразу.
func swapExecutable(exe, staged string) error {
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	if err := os.Rename(staged, exe); err != nil {
		return fmt.Errorf("не удалось заменить программу: %w", err)
	}
	if exec.Command("systemctl", "is-enabled", "compute-node").Run() == nil {
		_ = exec.Command("systemctl", "restart", "compute-node").Start()
	}
	return nil
}
