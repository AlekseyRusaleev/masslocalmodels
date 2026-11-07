//go:build windows

package worker

import (
	"syscall"
	"unsafe"
)

// memoryStatusWindows — MEMORYSTATUSEX из kernel32.
//
// Зачем свой вызов, а не PowerShell: проверка памяти выполняется перед стартом
// шард-группы, а запуск внешнего процесса занимает сотни миллисекунд и сам
// потребляет память. Здесь нужен точный ответ за микросекунды.
type memoryStatusWindows struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// MemoryStatus возвращает свободную физическую память и свободный запас
// фиксации (commit). Второе важнее первого: под WDDM драйвер не может
// закрепить память, когда commit-лимит исчерпан, и CUDA отвечает
// «cudaMalloc failed: out of memory» при свободной видеопамяти.
func MemoryStatus() (freePhysBytes, freeCommitBytes uint64, err error) {
	var status memoryStatusWindows
	status.Length = uint32(unsafe.Sizeof(status))
	ok, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return 0, 0, callErr
		}
		return 0, 0, syscall.EINVAL
	}
	return status.AvailPhys, status.AvailPageFile, nil
}
