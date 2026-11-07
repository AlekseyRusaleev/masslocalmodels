//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

// Агент запускается дочерним процессом без консольного окна, а в трее живёт
// это приложение. Так у пользователя нет постоянно открытой командной строки,
// но виден статус узла и есть управление.

const createNoWindow = 0x08000000

var (
	agentMu   sync.Mutex
	agentProc *exec.Cmd
)

// agentBinaryPath — путь к самому приложению: агент и трей теперь один файл.
func agentBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "compute-node.exe"
	}
	return exe
}

// agentLogPath — журнал агента рядом с конфигурацией.
func agentLogPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "logs", "worker-agent.log")
}

// startAgent запускает агент, если он ещё не запущен.
// Пустая модель — нормальный старт: долю весов пришлёт платформа.
func startAgent(configPath, model string) error {
	if configPath == "" {
		return fmt.Errorf("нет пути к конфигурации узла")
	}

	agentMu.Lock()
	defer agentMu.Unlock()
	if agentProc != nil && agentProc.Process != nil {
		return nil
	}

	args := []string{"run", "--config", configPath, "--log", agentLogPath(configPath)}
	if model != "" {
		args = append(args, "--model", model)
	}
	command := exec.Command(agentBinaryPath(), args...)
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("не удалось запустить агент: %w", err)
	}

	agentProc = command
	state.log(fmt.Sprintf("агент запущен (PID %d), окно консоли скрыто", command.Process.Pid))

	go func(proc *exec.Cmd) {
		err := proc.Wait()
		agentMu.Lock()
		if agentProc == proc {
			agentProc = nil
		}
		agentMu.Unlock()
		if err != nil {
			state.log("агент остановился: " + err.Error())
		} else {
			state.log("агент остановился")
		}
	}(command)

	return nil
}

// stopAgent останавливает агент, если он запущен.
func stopAgent() {
	agentMu.Lock()
	proc := agentProc
	agentProc = nil
	agentMu.Unlock()

	if proc == nil || proc.Process == nil {
		return
	}
	_ = proc.Process.Kill()
	state.log("агент остановлен вручную")
}

// agentRunning сообщает, работает ли агент.
func agentRunning() bool {
	agentMu.Lock()
	defer agentMu.Unlock()
	return agentProc != nil && agentProc.Process != nil
}

// supervisorArgs — аргументы автозапуска: в трее, без окна.
func supervisorArgs(configPath, model string) string {
	return fmt.Sprintf(`--run --config "%s" --model "%s"`, configPath, model)
}
