//go:build !windows

package workercfg

import "errors"

// На Linux эквивалента DPAPI в стандартной библиотеке нет, а тянуть
// keyring-зависимости ради одного поля не хочется. Секрет хранится в файле с
// правами 0600 — это защищает от других пользователей машины, но не от копии
// файла вместе с машиной. Честно сообщаем это в status.

// ErrNoSecretProtection означает, что платформа не умеет защищать секрет.
var ErrNoSecretProtection = errors.New("secret protection is not available on this platform")

// ProtectSecret на этой платформе не поддерживается.
func ProtectSecret([]byte) ([]byte, error) { return nil, ErrNoSecretProtection }

// UnprotectSecret на этой платформе не поддерживается.
func UnprotectSecret([]byte) ([]byte, error) { return nil, ErrNoSecretProtection }

// SecretProtection сообщает, чем защищён секрет.
func SecretProtection() string { return "права файла 0600 (без шифрования)" }
