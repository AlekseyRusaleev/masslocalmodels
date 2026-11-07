package download

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// downloadIdle — сколько ждать следующий байт. Дольше этого соединение
// считается зависшим: иначе полоска стоит на нуле, пока сокет молчит.
const downloadIdle = 20 * time.Second

// errStalled — заголовки пришли, а тело не двигается.
var errStalled = errors.New("download: stalled")

// httpClient качает файлы отдельными соединениями HTTP/1.1.
//
// Два ограничения, оба проверены на адресе головы:
//   - один HTTP/2-канал на несколько Range зависает: куски создаются и остаются по 0 байт;
//   - копия стандартного транспорта сохраняет предложение «h2» в TLS, но снимает
//     обработчик HTTP/2. Сервер отвечает кадрами HTTP/2, а клиент читает их как HTTP/1
//     и обрывает загрузку с «malformed HTTP response».
//
// Поэтому транспорт создаётся заново и в TLS называет только http/1.1.
var httpClient = &http.Client{
	Transport: http1Transport(),
}

func http1Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       &tls.Config{NextProtos: []string{"http/1.1"}},
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		DisableCompression:    true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// newRequest собирает GET-запрос с нашим User-Agent.
func newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("download: build request: %w", err)
	}
	req.Header.Set("User-Agent", "compute-network-worker")
	return req, nil
}

// httpDo выполняет запрос через клиент загрузок, без HTTP/2.
func httpDo(req *http.Request) (*http.Response, error) {
	return httpClient.Do(req)
}

// 部分档案 — куски файла, скачиваемые параллельно.
type chunk struct {
	index int
	start int64
	end   int64
	path  string
}

// downloadParallel качает файл в streams потоков по диапазонам.
// Возвращает количество байт и признак успеха; при любой ошибке части
// удаляются, чтобы не оставить мусор.
func downloadParallel(ctx context.Context, opts Options, partPath string, size int64, streams int) (Result, error) {
	started := time.Now()
	if streams < 2 {
		streams = 2
	}
	if streams > 16 {
		streams = 16
	}

	chunkSize := size / int64(streams)
	chunks := make([]chunk, 0, streams)
	for i := 0; i < streams; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == streams-1 {
			end = size - 1
		}
		chunks = append(chunks, chunk{
			index: i,
			start: start,
			end:   end,
			path:  fmt.Sprintf("%s.chunk-%02d", partPath, i),
		})
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		firstErr   error
		done       int64
		lastReport time.Time
	)
	report := func(snapshot int64) {
		if opts.Progress == nil {
			return
		}
		mu.Lock()
		now := time.Now()
		if snapshot < size && now.Sub(lastReport) < 200*time.Millisecond {
			mu.Unlock()
			return
		}
		lastReport = now
		mu.Unlock()
		opts.Progress(snapshot, size)
	}

	for _, c := range chunks {
		wg.Add(1)
		go func(c chunk) {
			defer wg.Done()
			if pctx.Err() != nil {
				return
			}
			_, err := fetchRange(pctx, opts, c, func(n int64) {
				mu.Lock()
				done += n
				snapshot := done
				mu.Unlock()
				report(snapshot)
			})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(c)
	}
	wg.Wait()

	if firstErr != nil {
		for _, c := range chunks {
			_ = os.Remove(c.path)
		}
		return Result{}, firstErr
	}
	if ctx.Err() != nil {
		for _, c := range chunks {
			_ = os.Remove(c.path)
		}
		return Result{}, ctx.Err()
	}

	if err := assemble(partPath, chunks); err != nil {
		for _, c := range chunks {
			_ = os.Remove(c.path)
		}
		return Result{}, err
	}

	sum, err := FileSHA256(partPath)
	if err != nil {
		return Result{}, err
	}
	if opts.ExpectedSHA != "" && !equalFold(sum, opts.ExpectedSHA) {
		return Result{SHA256: sum}, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, sum, opts.ExpectedSHA)
	}
	if err := os.Rename(partPath, opts.DestPath); err != nil {
		return Result{}, fmt.Errorf("download: rename: %w", err)
	}

	elapsed := time.Since(started)
	avg := 0.0
	if elapsed > 0 {
		avg = float64(size) / elapsed.Seconds()
	}
	return Result{
		Path:     opts.DestPath,
		Bytes:    size,
		SHA256:   sum,
		Elapsed:  elapsed,
		AvgBytes: avg,
	}, nil
}

// fetchRange скачивает один диапазон байт в отдельный файл.
// onBytes вызывается по мере записи, а не один раз в конце куска.
func fetchRange(ctx context.Context, opts Options, c chunk, onBytes func(int64)) (int64, error) {
	reqCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	req, err := newRequest(reqCtx, opts.URL)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.start, c.end))

	resp, err := httpDo(req)
	if err != nil {
		return 0, fmt.Errorf("download: chunk %d: %w", c.index, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 206 {
		return 0, fmt.Errorf("download: chunk %d: unexpected status %d", c.index, resp.StatusCode)
	}

	file, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("download: chunk %d: open: %w", c.index, err)
	}

	written, copyErr := copyWithIdle(file, resp.Body, downloadIdle, func(n int) {
		if onBytes != nil {
			onBytes(int64(n))
		}
	})
	closeErr := file.Close()
	if copyErr != nil {
		return written, fmt.Errorf("download: chunk %d: %w", c.index, copyErr)
	}
	if closeErr != nil {
		return written, fmt.Errorf("download: chunk %d: close: %w", c.index, closeErr)
	}
	return written, nil
}

// assemble склеивает куски в один файл по порядку и удаляет части.
func assemble(partPath string, chunks []chunk) error {
	sorted := make([]chunk, len(chunks))
	copy(sorted, chunks)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].index < sorted[j].index })

	out, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("download: assemble open: %w", err)
	}
	defer out.Close()

	for _, c := range sorted {
		in, err := os.Open(c.path)
		if err != nil {
			return fmt.Errorf("download: assemble chunk %d: %w", c.index, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			return fmt.Errorf("download: assemble copy: %w", err)
		}
		in.Close()
		_ = os.Remove(c.path)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("download: assemble sync: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("download: assemble close: %w", err)
	}
	// проверяем, что размер совпал
	info, err := os.Stat(partPath)
	if err != nil {
		return fmt.Errorf("download: assemble stat: %w", err)
	}
	if info.Size() != chunks[len(chunks)-1].end+1 {
		return errors.New("download: assembled size mismatch")
	}
	_ = filepath.Clean(partPath)
	return nil
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return string([]byte(lower(a))) == string([]byte(lower(b)))
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
