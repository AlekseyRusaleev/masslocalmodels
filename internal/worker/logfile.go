package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/private-compute/network/internal/workercfg"
)

// RotatingFile — файл журнала с ротацией по размеру.
//
// Зачем: агент работает постоянно, в том числе как задача планировщика с
// перенаправленным выводом. Без ротации лог на диске растёт бесконечно.
type RotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	file    *os.File
	size    int64
}

// OpenRotatingFile открывает файл для дозаписи, создавая каталог при необходимости.
func OpenRotatingFile(path string, maxSize int64, keep int) (*RotatingFile, error) {
	if path == "" {
		return nil, fmt.Errorf("log file path is empty")
	}
	if maxSize <= 0 {
		maxSize = 10 << 20
	}
	if keep <= 0 {
		keep = 3
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	r := &RotatingFile{path: path, maxSize: maxSize, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

// open открывает файл и запоминает текущий размер.
func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	if info, err := f.Stat(); err == nil {
		r.size = info.Size()
	}
	r.file = f
	return nil
}

// Write дописывает строку и при превышении лимита прокручивает файл.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size+int64(len(p)) > r.maxSize {
		r.rotate()
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate переименовывает файл в .1, сдвигая старые копии, и начинает новый.
func (r *RotatingFile) rotate() {
	_ = r.file.Close()
	r.file = nil

	for i := r.keep - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", r.path, i)
		next := fmt.Sprintf("%s.%d", r.path, i+1)
		_ = os.Rename(old, next)
	}
	_ = os.Rename(r.path, r.path+".1")

	if err := r.open(); err != nil {
		r.size = 0
	}
}

// Close закрывает файл.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// DefaultLogPath — путь журнала агента по умолчанию (рядом с конфигурацией).
func DefaultLogPath() string {
	return filepath.Join(filepath.Dir(workercfg.DefaultPath()), "logs", "worker-agent.log")
}
