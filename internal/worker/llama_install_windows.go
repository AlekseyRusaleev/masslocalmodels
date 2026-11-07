//go:build windows

package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/private-compute/network/internal/download"
)

// Архивы официальной сборки b12167. Хеши — digest релиза ggml-org/llama.cpp.
const (
	llamaBin134URL  = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/llama-b12167-bin-win-cuda-13.4-x64.zip"
	llamaBin134SHA  = "ed57938edd216a2f6ddec2d0d623983c3db3dde7301baca6cf5f24102ddf8cf7"
	llamaCUDA134URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/cudart-llama-bin-win-cuda-13.4-x64.zip"
	llamaCUDA134SHA = "738f8c251ac22b70c3ae6f83a10cf222725df0395246a2cf58f32bdb85fbe668"

	llamaBin124URL  = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/llama-b12167-bin-win-cuda-12.4-x64.zip"
	llamaBin124SHA  = "2fcea9d2e62b481cfa7a7893a915d15962cca7fc86a9ef5fab83bcde54bfd411"
	llamaCUDA124URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/cudart-llama-bin-win-cuda-12.4-x64.zip"
	llamaCUDA124SHA = "8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6"
)

func installLlama(ctx context.Context, opts EnsureLlamaOptions) (string, error) {
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(fmt.Sprintf(format, args...))
		}
	}
	progress := func(percent int, status string) {
		if opts.Progress != nil {
			opts.Progress(percent, status)
		}
	}

	dest := llamaInstallDir()
	if dest == "" {
		return "", fmt.Errorf("нет каталога для llama.cpp")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	family := llamaCUDAFamily(queryCUDAVersion(ctx))
	binURL, binSHA, cudaURL, cudaSHA := llamaBin134URL, llamaBin134SHA, llamaCUDA134URL, llamaCUDA134SHA
	if family == "12.4" {
		binURL, binSHA, cudaURL, cudaSHA = llamaBin124URL, llamaBin124SHA, llamaCUDA124URL, llamaCUDA124SHA
	}
	logf("скачиваю llama.cpp %s, CUDA %s", llamaRelease, family)

	if !llamaDirComplete(dest) {
		if err := fetchAndExtract(ctx, binURL, binSHA, dest, func(done, total int64) {
			if total > 0 {
				progress(4, fmt.Sprintf("скачиваю llama.cpp: %d%%", done*100/total))
			}
		}); err != nil {
			return "", fmt.Errorf("не скачан llama.cpp: %w", err)
		}
	}
	if !llamaRuntimeReady(dest) {
		progress(8, "скачиваю CUDA для llama.cpp")
		if err := fetchAndExtract(ctx, cudaURL, cudaSHA, dest, func(done, total int64) {
			if total > 0 {
				progress(8, fmt.Sprintf("скачиваю CUDA для llama.cpp: %d%%", done*100/total))
			}
		}); err != nil {
			return "", fmt.Errorf("не скачан CUDA-рантайм llama.cpp: %w", err)
		}
	}
	if !llamaRuntimeReady(dest) {
		return "", fmt.Errorf("в архиве llama.cpp нет llama-server, ggml-rpc-server или CUDA")
	}
	logf("llama.cpp установлена: %s", dest)
	return dest, nil
}

func fetchAndExtract(ctx context.Context, url, sha, dest string, progress func(done, total int64)) error {
	archive := filepath.Join(os.TempDir(), "compute-network-"+filepath.Base(url))
	if _, err := download.Fetch(ctx, download.Options{
		URL:         url,
		DestPath:    archive,
		ExpectedSHA: sha,
		Streams:     4,
		Retries:     3,
		Progress:    progress,
	}); err != nil {
		return err
	}
	if err := extractZip(archive, dest); err != nil {
		return err
	}
	_ = os.Remove(archive)
	return nil
}
