//go:build windows

package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("не удалось определить путь программы: %w", err)
	}
	return exe, nil
}

// swapExecutable дожидается выхода этой программы и подменяет файл.
// Сам файл занят, пока процесс жив, поэтому замену делает отдельный процесс.
func swapExecutable(exe, staged string) error {
	script := filepath.Join(os.TempDir(), "compute-node-update.ps1")
	body := fmt.Sprintf(`$target = %d
$deadline = (Get-Date).AddSeconds(40)
while ((Get-Process -Id $target -ErrorAction SilentlyContinue) -and ((Get-Date) -lt $deadline)) {
    Start-Sleep -Milliseconds 200
}
if (Get-Process -Id $target -ErrorAction SilentlyContinue) { exit 1 }
Start-Sleep -Milliseconds 400
Move-Item -LiteralPath %s -Destination %s -Force
$proc = Start-Process -FilePath %s -WindowStyle Normal -PassThru
Start-Sleep -Milliseconds 800
$shell = New-Object -ComObject WScript.Shell
[void]$shell.AppActivate($proc.Id)
`, os.Getpid(), psQuote(staged), psQuote(exe), psQuote(exe))
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		return fmt.Errorf("не удалось подготовить обновление: %w", err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить установку обновления: %w", err)
	}
	return nil
}

func psArgs(args []string) string {
	if len(args) == 0 {
		return "@()"
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = psQuote(arg)
	}
	return "@(" + strings.Join(quoted, ", ") + ")"
}
