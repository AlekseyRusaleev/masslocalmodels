// Package download — загрузка артефактов модели прямо из источника.
//
// Зачем: по плану (5.3-5.5) узел скачивает только свои файлы напрямую из
// репозитория или S3, а не получает их от координатора. Проверено измерением:
// передача 1.2 GB через координатора по домашнему каналу занимает десятки минут,
// поэтому вес должен идти от источника к узлу, минуя Control Plane.
//
// Свойства: возобновление по Range, проверка SHA-256, атомарная замена файла,
// параллельные диапазоны для крупных файлов и повторные попытки.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Options — параметры загрузки.
type Options struct {
	URL         string
	DestPath    string
	ExpectedSHA string        // hex, опционально
	Timeout     time.Duration // на весь запрос; 0 — без ограничения
	Retries     int
	Streams     int // параллельные диапазоны; >1 включает многопоточную загрузку
	Progress    func(done, total int64)
}

// parallelThreshold — минимальный размер, при котором многопоточность оправдана.
// Переменная, а не константа: тесты уменьшают порог.
var parallelThreshold int64 = 32 << 20

// Result — итог загрузки.
type Result struct {
	Path      string
	Bytes     int64
	SHA256    string
	Resumed   bool
	Elapsed   time.Duration
	AvgBytes  float64
	ClientSHA string
}

// ErrChecksumMismatch означает, что скачанный файл не совпал с ожидаемым хешем.
var ErrChecksumMismatch = errors.New("download: checksum mismatch")

// Fetch скачивает файл, при необходимости продолжая прерванную загрузку.
func Fetch(ctx context.Context, opts Options) (Result, error) {
	if opts.URL == "" {
		return Result{}, errors.New("download: url is required")
	}
	if opts.DestPath == "" {
		return Result{}, errors.New("download: destination is required")
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}

	if err := os.MkdirAll(filepath.Dir(opts.DestPath), 0o755); err != nil {
		return Result{}, fmt.Errorf("download: create dir: %w", err)
	}

	partPath := opts.DestPath + ".part"
	var lastErr error

	for attempt := 1; attempt <= opts.Retries; attempt++ {
		result, err := fetchOnce(ctx, opts, partPath)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if errors.Is(err, ErrChecksumMismatch) {
			_ = os.Remove(partPath)
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return Result{}, fmt.Errorf("download: giving up after %d attempts: %w", opts.Retries, lastErr)
}

// fetchOnce выполняет одну попытку загрузки с продолжением по Range.
func fetchOnce(ctx context.Context, opts Options, partPath string) (Result, error) {
	started := time.Now()

	var offset int64
	if info, err := os.Stat(partPath); err == nil {
		offset = info.Size()
	}

	// Многопоточная загрузка имеет смысл только с нуля и для крупных файлов.
	// Если куски зависли, тот же заход продолжается одним потоком.
	if offset == 0 && opts.Streams > 1 {
		if size, ok := probeSize(ctx, opts); ok && size >= parallelThreshold {
			result, err := downloadParallel(ctx, opts, partPath, size, opts.Streams)
			if err == nil {
				return result, nil
			}
			if ctx.Err() != nil || errors.Is(err, ErrChecksumMismatch) {
				return Result{}, err
			}
		}
	}

	reqCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("download: build request: %w", err)
	}
	req.Header.Set("User-Agent", "compute-network-worker")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := httpDo(req)
	if err != nil {
		return Result{}, fmt.Errorf("download: request: %w", err)
	}
	defer resp.Body.Close()

	resumed := offset > 0 && resp.StatusCode == http.StatusPartialContent
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0 // сервер не поддержал Range — пишем с начала
	case http.StatusPartialContent:
		// продолжаем
	case http.StatusNotFound:
		return Result{}, fmt.Errorf("download: %s not found", opts.URL)
	default:
		return Result{}, fmt.Errorf("download: unexpected status %d", resp.StatusCode)
	}

	total := int64(-1)
	if resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}

	flag := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	file, err := os.OpenFile(partPath, flag, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("download: open part: %w", err)
	}

	written := offset
	copied, copyErr := copyWithIdle(file, resp.Body, downloadIdle, func(n int) {
		written += int64(n)
		if opts.Progress != nil {
			opts.Progress(written, total)
		}
	})
	_ = copied
	if copyErr != nil {
		file.Close()
		return Result{}, fmt.Errorf("download: read: %w", copyErr)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return Result{}, fmt.Errorf("download: sync: %w", err)
	}
	if err := file.Close(); err != nil {
		return Result{}, fmt.Errorf("download: close: %w", err)
	}

	sum, err := FileSHA256(partPath)
	if err != nil {
		return Result{}, err
	}

	if opts.ExpectedSHA != "" && !strings.EqualFold(sum, opts.ExpectedSHA) {
		return Result{SHA256: sum, ClientSHA: opts.ExpectedSHA}, fmt.Errorf(
			"%w: got %s, want %s", ErrChecksumMismatch, sum, opts.ExpectedSHA)
	}

	if err := os.Rename(partPath, opts.DestPath); err != nil {
		return Result{}, fmt.Errorf("download: rename: %w", err)
	}

	elapsed := time.Since(started)
	avg := 0.0
	if elapsed > 0 {
		avg = float64(written) / elapsed.Seconds()
	}

	return Result{
		Path:     opts.DestPath,
		Bytes:    written,
		SHA256:   sum,
		Resumed:  resumed,
		Elapsed:  elapsed,
		AvgBytes: avg,
	}, nil
}

// probeSize проверяет поддержку Range и узнаёт полный размер файла.
// Возвращает false, если сервер диапазоны не поддерживает — тогда качаем в один поток.
func probeSize(ctx context.Context, opts Options) (int64, bool) {
	reqCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	req, err := newRequest(reqCtx, opts.URL)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := httpDo(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusPartialContent {
		return 0, false
	}

	// Content-Range: bytes 0-0/12345
	contentRange := resp.Header.Get("Content-Range")
	if contentRange == "" {
		return 0, false
	}
	slash := strings.LastIndex(contentRange, "/")
	if slash < 0 {
		return 0, false
	}
	size, err := strconv.ParseInt(strings.TrimSpace(contentRange[slash+1:]), 10, 64)
	if err != nil || size <= 0 {
		return 0, false
	}
	return size, true
}

// FileSHA256 считает SHA-256 файла потоком, без чтения целиком в память.
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("download: open for hash: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("download: hash: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyWithIdle копирует поток и прерывает копирование, если байты перестали
// приходить. onBytes получает размер каждой записанной порции.
func copyWithIdle(dst io.Writer, src io.Reader, idle time.Duration, onBytes func(int)) (int64, error) {
	buf := make([]byte, 256<<10)
	var total int64
	for {
		n, err := readIdle(src, buf, idle)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return total, writeErr
			}
			total += int64(n)
			if onBytes != nil {
				onBytes(n)
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func readIdle(r io.Reader, buf []byte, idle time.Duration) (int, error) {
	type outcome struct {
		n   int
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		n, err := r.Read(buf)
		done <- outcome{n, err}
	}()
	timer := time.NewTimer(idle)
	defer timer.Stop()
	select {
	case out := <-done:
		return out.n, out.err
	case <-timer.C:
		return 0, errStalled
	}
}

// ParseSize разбирает размер вида "5G", "500M", "1048576" в байты.
func ParseSize(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	if raw == "" {
		return 0, errors.New("download: empty size")
	}
	multiplier := int64(1)
	switch raw[len(raw)-1] {
	case 'K':
		multiplier = 1 << 10
		raw = raw[:len(raw)-1]
	case 'M':
		multiplier = 1 << 20
		raw = raw[:len(raw)-1]
	case 'G':
		multiplier = 1 << 30
		raw = raw[:len(raw)-1]
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("download: invalid size %q: %w", raw, err)
	}
	return value * multiplier, nil
}
