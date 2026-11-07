//go:build linux

package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/private-compute/network/internal/download"
)

const (
	llamaLinuxBin134URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/llama-b12167-bin-ubuntu-cuda-13.4-x64.tar.gz"
	llamaLinuxBin134SHA = "adf9d677741afd3bcf1f8debea51926b0341cb94c2758afa727cb3a38f0b2ac4"
	llamaLinuxCUDA134URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/cudart-llama-b12167-bin-ubuntu-cuda-13.4-x64.tar.gz"
	llamaLinuxCUDA134SHA = "db0c22114db5751a12ff1c970ceed6e805fb0dbde12f57ca7ddc28414079b0f5"

	llamaLinuxBin128URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/llama-b12167-bin-ubuntu-cuda-12.8-x64.tar.gz"
	llamaLinuxBin128SHA = "d4283bf27a69919d196b67c7c147844d53820baab2bf5e8fef5b2aabe1a74c1f"
	llamaLinuxCUDA128URL = "https://github.com/ggml-org/llama.cpp/releases/download/b12167/cudart-llama-b12167-bin-ubuntu-cuda-12.8-x64.tar.gz"
	llamaLinuxCUDA128SHA = "f6e956c305e9c2d963e3d26eb975af168acf2832fbf7696cf3ecaa0e183e243d"
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

	family := linuxCUDAFamily(queryCUDAVersion(ctx))
	binURL, binSHA, cudaURL, cudaSHA := llamaLinuxBin134URL, llamaLinuxBin134SHA, llamaLinuxCUDA134URL, llamaLinuxCUDA134SHA
	if family == "12.8" {
		binURL, binSHA, cudaURL, cudaSHA = llamaLinuxBin128URL, llamaLinuxBin128SHA, llamaLinuxCUDA128URL, llamaLinuxCUDA128SHA
	}
	logf("скачиваю llama.cpp %s, CUDA %s", llamaRelease, family)

	if locateLlamaDir(dest) == "" {
		if err := fetchAndExtractTar(ctx, binURL, binSHA, dest, func(done, total int64) {
			if total > 0 {
				progress(4, fmt.Sprintf("скачиваю llama.cpp: %d%%", done*100/total))
			}
		}); err != nil {
			return "", fmt.Errorf("не скачан llama.cpp: %w", err)
		}
	}
	if !llamaRuntimeReady(locateOrDest(dest)) {
		progress(8, "скачиваю CUDA для llama.cpp")
		if err := fetchAndExtractTar(ctx, cudaURL, cudaSHA, dest, func(done, total int64) {
			if total > 0 {
				progress(8, fmt.Sprintf("скачиваю CUDA для llama.cpp: %d%%", done*100/total))
			}
		}); err != nil {
			return "", fmt.Errorf("не скачан CUDA-рантайм llama.cpp: %w", err)
		}
	}
	dir := locateLlamaDir(dest)
	if dir == "" {
		return "", fmt.Errorf("в архиве llama.cpp нет llama-server, ggml-rpc-server или CUDA")
	}
	for _, name := range []string{"llama-server", "ggml-rpc-server"} {
		_ = os.Chmod(filepath.Join(dir, name), 0o755)
	}
	logf("llama.cpp установлена: %s", dir)
	return dir, nil
}

func locateOrDest(dest string) string {
	if dir := locateLlamaDir(dest); dir != "" {
		return dir
	}
	return dest
}

func fetchAndExtractTar(ctx context.Context, url, sha, dest string, progress func(done, total int64)) error {
	archive := filepath.Join(os.TempDir(), filepath.Base(url))
	if _, err := download.Fetch(ctx, download.Options{
		URL:         url,
		DestPath:    archive,
		ExpectedSHA: sha,
		Retries:     3,
		Streams:     4,
		Progress:    progress,
	}); err != nil {
		return err
	}
	defer os.Remove(archive)
	return extractTarGz(archive, dest)
}
