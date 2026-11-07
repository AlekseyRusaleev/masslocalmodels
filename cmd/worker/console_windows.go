//go:build windows

package main

import (
	"os"
	"syscall"
)

var (
	procAttachConsole = syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	procGetStdHandle  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetStdHandle")
)

const (
	attachParentProcess = ^uintptr(0)  // (DWORD)-1
	stdioOutput         = ^uintptr(10) // STD_OUTPUT_HANDLE = -11
	stdioError          = ^uintptr(11) // STD_ERROR_HANDLE = -12
	stdioInput          = ^uintptr(9)  // STD_INPUT_HANDLE = -10
)

// stdHandleValid сообщает, что стандартный поток уже кем-то назначен
// (перенаправление в файл или канал): тогда трогать его нельзя.
func stdHandleValid(which uintptr) bool {
	handle, _, _ := procGetStdHandle.Call(which)
	// INVALID_HANDLE_VALUE = -1, NULL = 0 — оба означают «потока нет».
	return handle != 0 && handle != ^uintptr(0)
}

// enableConsole подключает стандартные потоки к консоли родителя.
//
// Программа собрана как оконная, чтобы у планировщика не появлялось окно
// консоли. Команды агента должны печатать результат, поэтому при запуске из
// терминала потоки привязываются к нему. Если поток уже перенаправлен
// (канал, файл), он сохраняется как есть — иначе вывод уйдёт не туда.
func enableConsole() {
	if !stdHandleValid(stdioOutput) && attachSucceeds() {
		reopenStdio()
		return
	}
	if !stdHandleValid(stdioError) || !stdHandleValid(stdioInput) {
		attachSucceeds()
	}
}

// attachSucceeds пытается подключиться к консоли родителя.
func attachSucceeds() bool {
	attached, _, _ := procAttachConsole.Call(attachParentProcess)
	return attached != 0
}

// reopenStdio открывает потоки на консоли.
func reopenStdio() {
	if handle, err := syscall.Open("CONOUT$", syscall.O_WRONLY, 0); err == nil {
		os.Stdout = os.NewFile(uintptr(handle), "CONOUT$")
	}
	if handle, err := syscall.Open("CONOUT$", syscall.O_WRONLY, 0); err == nil {
		os.Stderr = os.NewFile(uintptr(handle), "CONOUT$")
	}
	if handle, err := syscall.Open("CONIN$", syscall.O_RDONLY, 0); err == nil {
		os.Stdin = os.NewFile(uintptr(handle), "CONIN$")
	}
}
