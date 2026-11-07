//go:build windows

package worker

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// registerLogonTask создаёт задачу планировщика при входе пользователя.
//
// Через schtasks.exe задача ONLOGON требует прав администратора, поэтому
// используется модуль ScheduledTasks: он умеет создавать задачу в профиле
// текущего пользователя с интерактивным входом и ограниченными правами.
// Вывод не перехватывается — окно консоли подавлено.
func registerLogonTask(name, execPath, arguments string) error {
	user := strings.TrimSpace(os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME"))
	if user == `\` {
		return fmt.Errorf("не удалось определить пользователя для автозапуска")
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$action = New-ScheduledTaskAction -Execute %s -Argument %s -WorkingDirectory %s
$trigger = New-ScheduledTaskTrigger -AtLogOn -User %s
$settings = New-ScheduledTaskSettingsSet -Hidden -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId %s -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName %s -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Description 'Worker Agent: heartbeat and inference jobs' -Force | Out-Null
Start-ScheduledTask -TaskName %s
`,
		psQuote(execPath), psQuote(arguments), psQuote(workingDir(execPath)),
		psQuote(user), psQuote(user), psQuote(name), psQuote(name))

	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", script)
	quiet(command)

	// Вывод нужен только для текста ошибки, поэтому читаем его после запуска.
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 400 {
			message = message[:400]
		}
		return fmt.Errorf("не удалось включить автозапуск: %v: %s", err, message)
	}
	return nil
}

// RegisterSupervisorAutostart ставит в автозапуск приложение с треем.
//
// В отличие от RunAutostart здесь запускается не консольный агент, а оконная
// программа: она стартует в трее и сама поднимает агент без окна консоли.
func RegisterSupervisorAutostart(execPath, configPath, model, taskName string) error {
	if execPath == "" || configPath == "" {
		return fmt.Errorf("нужен путь к программе и к конфигурации")
	}
	if taskName == "" {
		taskName = DefaultTaskName
	}
	arguments := fmt.Sprintf(`--run --config "%s" --model "%s"`, configPath, model)
	return registerLogonTask(taskName, execPath, arguments)
}

// psQuote экранирует строку для одинарных кавычек PowerShell.
func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// workingDir возвращает каталог программы: относительные пути в задаче
// планировщика считаются от него.
func workingDir(execPath string) string {
	if index := strings.LastIndexAny(execPath, `\/`); index > 0 {
		return execPath[:index]
	}
	return "."
}
