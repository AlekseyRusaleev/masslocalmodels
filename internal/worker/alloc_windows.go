//go:build windows

package worker

import (
	"syscall"
	"unsafe"
)

var getCompressedFileSizeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCompressedFileSizeW")

// allocatedFileBytes возвращает место, которое файл занимает на диске.
// У заранее созданного файла длина больше, чем реально записанные байты.
func allocatedFileBytes(path string) int64 {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var high uint32
	low, _, callErr := getCompressedFileSizeW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&high)))
	if low == 0xFFFFFFFF && callErr != syscall.Errno(0) {
		return 0
	}
	return int64(high)<<32 | int64(uint32(low))
}
