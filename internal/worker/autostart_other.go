//go:build !windows

package worker

import "fmt"

// registerLogonTask вне Windows не поддерживается: на сервере узел запускают
// через systemd, и это отдельный сценарий.
func registerLogonTask(name, execPath, arguments string) error {
	return fmt.Errorf("автозапуск через планировщик поддерживается только на Windows")
}
