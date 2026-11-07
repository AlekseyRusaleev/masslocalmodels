//go:build windows

package main

import (
	"unsafe"
)

var (
	procShellExecute = shell32.NewProc("ShellExecuteW")
	procGetConsole   = kernel32.NewProc("GetConsoleWindow")
)

// hideConsole убирает консольное окно: установщик — оконная программа, но
// Windows всё равно может показать консоль при запуске из cmd.
func hideConsole(console uintptr) {
	procShowWindow.Call(console, swHide)
}

// openExplorer открывает каталог в проводнике.
func openExplorer(dir string) {
	procShellExecute.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr(dir))),
		0, 0, swShow)
}
