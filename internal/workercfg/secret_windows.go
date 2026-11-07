//go:build windows

package workercfg

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Шифрование секрета узла средствами Windows (DPAPI).
//
// Зачем: файл конфигурации агента содержит секрет устройства. Если положить его
// рядом с программой открытым текстом, копия файла на другой машине продолжит
// работать. DPAPI привязывает шифрование к учётной записи и машине: скопированный
// конфиг на другом компьютере не расшифруется.

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree      = kernel32.NewProc("LocalFree")
)

// dataBlob повторяет структуру DATA_BLOB из wincrypt.h.
type dataBlob struct {
	size uint32
	data *byte
}

const (
	// CRYPTPROTECT_UI_FORBIDDEN — не показывать диалоги: агент работает без
	// интерактивного пользователя.
	cryptProtectUIForbidden = 0x1
)

// ProtectSecret шифрует данные для текущего пользователя этой машины.
func ProtectSecret(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, nil
	}
	in := dataBlob{size: uint32(len(plain)), data: &plain[0]}
	var out dataBlob

	result, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return nil, fmt.Errorf("protect secret: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))

	buf := make([]byte, out.size)
	copy(buf, unsafe.Slice(out.data, out.size))
	return buf, nil
}

// UnprotectSecret расшифровывает данные, зашифрованные ProtectSecret.
func UnprotectSecret(encrypted []byte) ([]byte, error) {
	if len(encrypted) == 0 {
		return nil, nil
	}
	in := dataBlob{size: uint32(len(encrypted)), data: &encrypted[0]}
	var out dataBlob

	result, _, err := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if result == 0 {
		return nil, fmt.Errorf("unprotect secret: %w", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))

	buf := make([]byte, out.size)
	copy(buf, unsafe.Slice(out.data, out.size))
	return buf, nil
}

// SecretProtection сообщает, чем защищён секрет: для вывода в status.
func SecretProtection() string { return "DPAPI (учётная запись Windows)" }
