//go:build windows

package worker

import (
	"os/exec"
	"syscall"
)

// createNoWindow — CREATE_NO_WINDOW: дочерний консольный процесс не получает
// собственного окна консоли.
const createNoWindow = 0x08000000

// quiet запускает дочернюю консольную утилиту без окна.
//
// Приложение узла собрано как оконное, поэтому у него самого консоли нет. Без
// этого флага Windows создаёт консоль для каждого дочернего процесса
// (nvidia-smi, powershell), и на экране мигает чёрное окно — в том числе каждые
// 30 секунд при сборе телеметрии.
func quiet(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
