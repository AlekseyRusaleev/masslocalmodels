package worker

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/private-compute/network/internal/workercfg"
)

// prepareAutomaticMesh включает разделение слоёв, если на машине уже есть
// llama.cpp и файл весов.
//
// Ручная настройка не переписывается. Пока платформа не собрала группу из
// двух таких узлов, чат по-прежнему обслуживается моделью целиком.
func prepareAutomaticMesh(cfg workercfg.Config) workercfg.Config {
	if cfg.Mesh != nil || strings.TrimSpace(cfg.Model) == "" {
		return cfg
	}
	llamaDir := findLlamaDir()
	weights := localWeightsFile(cfg.Model)
	if llamaDir == "" || weights == "" {
		return cfg
	}
	cfg.Mesh = &workercfg.Mesh{
		Role:     meshRoleAuto,
		Model:    cfg.Model,
		GGUFPath: weights,
		LlamaDir: llamaDir,
		Ctx:      NumCtxOf(cfg),
		Cache:    true,
		Tunnel:   true,
	}
	return cfg
}

// findLlamaDir ищет каталог, где лежат llama-server и ggml-rpc-server.
//
// Сначала явный путь, потом каталог, куда программа ставит сборку сама,
// и только затем старая ручная установка.
func findLlamaDir() string {
	if dir := officialLlamaDir(); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("COMPUTE_LLAMA_DIR")); dir != "" {
		if found := locateLlamaDir(dir); found != "" {
			return found
		}
	}
	if dir := locateLlamaDir(llamaInstallDir()); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(home, "llama-cpp-ifm")
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name(), "llama")
		if llamaRuntimeReady(dir) {
			return dir
		}
	}
	return ""
}

// locateLlamaDir находит каталог с обоими файлами: архив иногда кладёт их
// на уровень ниже корня распаковки.
func locateLlamaDir(root string) string {
	if llamaRuntimeReady(root) {
		return root
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if llamaRuntimeReady(dir) {
			return dir
		}
	}
	return ""
}

func llamaDirComplete(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	server := filepath.Join(dir, executableName("llama-server"))
	rpc := filepath.Join(dir, executableName("ggml-rpc-server"))
	_, serverErr := os.Stat(server)
	_, rpcErr := os.Stat(rpc)
	return serverErr == nil && rpcErr == nil
}

// localWeightsFile — GGUF, который Ollama уже скачал для этой модели.
func localWeightsFile(model string) string {
	digest, ok := WeightsDigest(model)
	if !ok {
		return ""
	}
	for _, dir := range ollamaModelDirs() {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, "blobs", "sha256-"+digest)
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return path
		}
	}
	return ""
}
