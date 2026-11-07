package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/private-compute/network/internal/download"
	"github.com/private-compute/network/internal/swarm"
	"github.com/private-compute/network/internal/workercfg"
)

// WeightFile — один файл, который этому компьютеру нужно скачать.
type WeightFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// WeightPlan — доля весов, которую платформа назначила этому компьютеру
// на текущий состав включённых участников.
type WeightPlan struct {
	RevisionID string       `json:"revision_id"`
	Model      string       `json:"model"`
	Keep       []string     `json:"keep"`
	Fetch      []WeightFile `json:"fetch"`
	Release    []string     `json:"release"`
	Missing    []string     `json:"missing,omitempty"`
	Short      []string     `json:"short,omitempty"`
}

// IsRetiredModel — пробная модель, которую больше не скачиваем и не объявляем.
func IsRetiredModel(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), swarm.RetiredModel)
}

// SyncWeights приводит каталог к назначенной доле.
//
// Скачиваются только файлы из fetch. Удаляются только имена из release,
// и только если это простой файл внутри каталога. Чужие файлы не трогаются.
func SyncWeights(ctx context.Context, dir string, fetch []WeightFile, release []string) error {
	return syncWeights(ctx, dir, fetch, release, func(ctx context.Context, file WeightFile, dest string) error {
		_, err := download.Fetch(ctx, download.Options{
			URL:         file.URL,
			DestPath:    dest,
			ExpectedSHA: file.SHA256,
			Streams:     8,
			Retries:     3,
		})
		return err
	})
}

func syncWeights(ctx context.Context, dir string, fetch []WeightFile, release []string, fetchFn func(context.Context, WeightFile, string) error) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("каталог весов: %w", err)
	}
	for _, file := range fetch {
		name, err := plainName(file.Name)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, name)
		if file.SHA256 != "" {
			if existing, err := download.FileSHA256(dest); err == nil && strings.EqualFold(existing, file.SHA256) {
				continue
			}
		}
		if err := fetchFn(ctx, file, dest); err != nil {
			return fmt.Errorf("скачивание %s: %w", name, err)
		}
	}
	keep := map[string]bool{}
	for _, file := range fetch {
		if name, err := plainName(file.Name); err == nil {
			keep[name] = true
		}
	}
	for _, raw := range release {
		name, err := plainName(raw)
		if err != nil {
			continue
		}
		if keep[name] {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("удаление %s: %w", name, err)
		}
	}
	return nil
}

// DownloadedWeightBytes — сколько байт весов уже лежит на диске.
//
// Считаются блобы Ollama и файлы, которые раздала платформа, вместе с
// незаконченной загрузкой: число растёт, пока файл ещё качается.
func DownloadedWeightBytes() int64 {
	return treeBytes(filepath.Join(ollamaModelDir(), "blobs")) + treeBytes(workercfg.DefaultModelDir())
}

func treeBytes(dir string) int64 {
	if strings.TrimSpace(dir) == "" || dir == "blobs" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// HeldPieceNames перечисляет файлы весов, которые уже лежат локально.
// Имена уходят на платформу, и по ним считается, кто сейчас раздаёт какой кусок.
func HeldPieceNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			nested, err := os.ReadDir(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			for _, file := range nested {
				if file.IsDir() || strings.HasSuffix(file.Name(), ".part") {
					continue
				}
				names = append(names, file.Name())
			}
			continue
		}
		if strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

// FetchDraftHead скачивает голову черновика текущей модели, если её ещё нет.
// Повторный запуск сверяет хеш и не качает файл заново.
func FetchDraftHead(ctx context.Context, report func(done, total int64)) (string, error) {
	dir := workercfg.DefaultModelDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("каталог головы: %w", err)
	}
	dest := filepath.Join(dir, DefaultDraftName)
	if DefaultDraftSHA256 != "" {
		if existing, err := download.FileSHA256(dest); err == nil && strings.EqualFold(existing, DefaultDraftSHA256) {
			return dest, nil
		}
	}
	_, err := download.Fetch(ctx, download.Options{
		URL:         DefaultDraftURL,
		DestPath:    dest,
		ExpectedSHA: DefaultDraftSHA256,
		// Один поток быстрее нескольких кусков: хранилище Hugging Face
		// на диапазонах отдаёт меньше, чем целым файлом.
		Streams:  1,
		Retries:  3,
		Progress: report,
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

var (
	draftDirOnce   sync.Once
	draftDirCached string
)

// DraftHeadProgress говорит, сколько байт головы уже лежит на диске.
// active — скачивание ещё идёт: есть незавершённый файл или его куски.
func DraftHeadProgress() (done, total int64, active bool) {
	total = DefaultDraftBytes
	draftDirOnce.Do(func() {
		draftDirCached = workercfg.DefaultModelDir()
	})
	dest := filepath.Join(draftDirCached, DefaultDraftName)
	if info, err := os.Stat(dest); err == nil && !info.IsDir() && info.Size() > 0 {
		return info.Size(), total, false
	}
	if info, err := os.Stat(dest + ".part"); err == nil && !info.IsDir() {
		done += info.Size()
		active = true
	}
	for i := 0; i < 16; i++ {
		info, err := os.Stat(fmt.Sprintf("%s.part.chunk-%02d", dest, i))
		if err != nil || info.IsDir() {
			continue
		}
		done += info.Size()
		active = true
	}
	return done, total, active
}

func plainName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("некорректное имя файла весов %q", name)
	}
	return name, nil
}

// HashBytes — короткая обёртка для тестов и локальной сверки.
func HashBytes(r io.Reader) (string, error) {
	sum := sha256.New()
	if _, err := io.Copy(sum, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
