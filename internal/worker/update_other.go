//go:build !windows && !linux

package worker

import "fmt"

func currentExecutable() (string, error) {
	return "", fmt.Errorf("обновление программы поддерживается на Windows")
}

func swapExecutable(exe, staged string) error {
	return fmt.Errorf("обновление программы поддерживается на Windows")
}
