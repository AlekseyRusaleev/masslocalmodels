package worker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OllamaModels возвращает модели, которые уже скачаны в локальный рантайм.
func OllamaModels(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OllamaEndpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("runtime http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var body struct {
		Models []struct {
			Name string `json:"name"`
			Tag  string `json:"tag"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("cannot read runtime model list: %w", err)
	}

	out := make([]string, 0, len(body.Models))
	for _, model := range body.Models {
		name := model.Name
		if name == "" && model.Tag != "" {
			name = model.Tag
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// modelManifestPath — где Ollama хранит манифест модели.
//
// Манифест — это JSON со списком слоёв: веса, шаблон, лицензия, параметры.
// Имя модели «qwen3:8b» разворачивается в registry.ollama.ai/library/qwen3/8b.
func modelManifestPath(model string) string {
	dir := ollamaModelDir()
	if dir == "" {
		return ""
	}
	registry := "registry.ollama.ai"
	namespace := "library"
	rest := model
	if idx := strings.LastIndex(rest, ":"); idx >= 0 {
		rest = rest[:idx]
	}
	parts := strings.Split(rest, "/")
	name := parts[len(parts)-1]
	tag := "latest"
	if idx := strings.LastIndex(model, ":"); idx >= 0 {
		tag = model[idx+1:]
	}
	switch {
	case len(parts) >= 3:
		registry, namespace, name = parts[0], parts[1], parts[2]
	case len(parts) == 2:
		namespace, name = parts[0], parts[1]
	}
	return filepath.Join(dir, "manifests", registry, namespace, name, tag)
}

// WeightsDigest возвращает sha256 файла весов модели.
//
// Важно не путать: digest модели в Ollama — это хэш манифеста, а не весов.
// Для аттестации нужен именно слой с весами (mediaType image.model): его хэш и
// есть sha256 GGUF-файла.
func WeightsDigest(model string) (string, bool) {
	path := modelManifestPath(model)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var manifest struct {
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", false
	}
	for _, layer := range manifest.Layers {
		if strings.Contains(layer.MediaType, "image.model") {
			digest := NormalizeWeightsDigest(layer.Digest)
			if digest == "" {
				return "", false
			}
			return digest, true
		}
	}
	return "", false
}

// NormalizeWeightsDigest убирает префикс sha256: и приводит к нижнему регистру.
func NormalizeWeightsDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}

// LocalModelDigests возвращает digest'ы весов всех скачанных моделей.
//
// Именно весов, а не манифеста: платформа сверяет их с манифестом каталога,
// где закреплён sha256 файла модели.
func LocalModelDigests(ctx context.Context) map[string]string {
	out := make(map[string]string, 4)

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OllamaEndpoint+"/api/tags", nil)
	if err != nil {
		return out
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return out
	}

	var body struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&body); err != nil {
		return out
	}

	for _, loaded := range body.Models {
		name := loaded.Name
		if name == "" {
			name = loaded.Model
		}
		if name == "" {
			continue
		}
		if digest, ok := WeightsDigest(name); ok {
			out[name] = digest
		}
	}
	return out
}

// verifyCache помнит результат самопроверки весов: хэширование 5 ГБ занимает
// секунды, поэтому одного раза на модель за запуск достаточно.
var (
	verifyMu    sync.Mutex
	verifyCache = map[string]bool{}
	verifyBusy  = map[string]bool{}
)

// VerifyLocalWeights пересчитывает sha256 файла весов и сверяет с digest.
//
// Рантайм сообщает digest сам, но это его слово. Здесь узел проверяет файл,
// который реально лежит на диске: Ollama хранит блобы как blobs/sha256-<digest>,
// поэтому имя файла и его содержимое можно сверить напрямую.
func VerifyLocalWeights(ctx context.Context, model, digest string) bool {
	digest = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if digest == "" {
		return false
	}

	verifyMu.Lock()
	if known, ok := verifyCache[model]; ok {
		verifyMu.Unlock()
		return known
	}
	if verifyBusy[model] {
		verifyMu.Unlock()
		return false
	}
	verifyBusy[model] = true
	verifyMu.Unlock()

	defer func() {
		verifyMu.Lock()
		delete(verifyBusy, model)
		verifyMu.Unlock()
	}()

	path := filepath.Join(ollamaModelDir(), "blobs", "sha256-"+digest)
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false
	}
	actual := hex.EncodeToString(hasher.Sum(nil))

	verifyMu.Lock()
	verifyCache[model] = actual == digest
	verifyMu.Unlock()
	return actual == digest
}

// ollamaModelDir — где рантайм хранит блобы весов.
func ollamaModelDir() string {
	if dir := os.Getenv("OLLAMA_MODELS"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ollama", "models")
}

// LoadedModel сообщает, какая модель сейчас загружена в рантайме и с каким
// digest весов.
//
// Это доказательство подлинности: платформа сверяет имя с запрошенным, а digest
// сохраняет, чтобы результат можно было проверить постфактум. Если рантайм не
// ответил, возвращаются пустые строки — «неизвестно» честнее выдумки.
func LoadedModel(ctx context.Context, wanted string) (name, digest string) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OllamaEndpoint+"/api/ps", nil)
	if err != nil {
		return "", ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", ""
	}

	var body struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", ""
	}

	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		return strings.TrimSuffix(value, ":latest")
	}
	target := normalize(wanted)
	for _, loaded := range body.Models {
		candidate := loaded.Name
		if candidate == "" {
			candidate = loaded.Model
		}
		if normalize(candidate) == target {
			if digest, ok := WeightsDigest(candidate); ok {
				return candidate, digest
			}
			return candidate, NormalizeWeightsDigest(loaded.Digest)
		}
	}
	// Загруженная модель не совпала с запрошенной: возвращаем первую, чтобы
	// платформа увидела расхождение, а не пустоту.
	if len(body.Models) > 0 {
		first := body.Models[0]
		name := first.Name
		if name == "" {
			name = first.Model
		}
		return name, first.Digest
	}
	return "", ""
}

// UnloadOllamaModels выгружает модели из памяти рантайма Ollama.
//
// Зачем: перед стартом шард-группы на той же машине нельзя держать в памяти
// вторую модель. Наблюдённый отказ: при ~1.1 ГБ свободной RAM llama-server
// умирал на выделении буфера, хотя видеопамять была свободна. Выгрузка
// возвращает и RAM, и VRAM без перезапуска сервисов.
//
// Возвращает имена выгруженных моделей (пусто — значит выгружать было нечего).
func UnloadOllamaModels(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OllamaEndpoint+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("runtime http %d", resp.StatusCode)
	}

	var loaded struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&loaded); err != nil {
		return nil, fmt.Errorf("cannot read loaded models: %w", err)
	}

	unloaded := make([]string, 0, len(loaded.Models))
	for _, model := range loaded.Models {
		name := model.Name
		if name == "" {
			name = model.Model
		}
		if name == "" {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"model": name, "keep_alive": 0})
		unloadCtx, unloadCancel := context.WithTimeout(ctx, 30*time.Second)
		unloadReq, err := http.NewRequestWithContext(unloadCtx, http.MethodPost,
			OllamaEndpoint+"/api/generate", bytes.NewReader(payload))
		if err != nil {
			unloadCancel()
			continue
		}
		unloadReq.Header.Set("Content-Type", "application/json")
		unloadResp, err := http.DefaultClient.Do(unloadReq)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(unloadResp.Body, 4096))
			unloadResp.Body.Close()
			if unloadResp.StatusCode < 300 {
				unloaded = append(unloaded, name)
			}
		}
		unloadCancel()
	}
	return unloaded, nil
}

// PullProgress — состояние загрузки модели для интерфейса.
type PullProgress struct {
	Status    string
	Completed int64
	Total     int64
}

// OllamaPull скачивает модель в локальный рантайм, печатая прогресс по статусам.
//
// Загрузка идёт напрямую с источника: через платформу гигабайты не проходят
// (ADR 0004). Таймаут рассчитан на медленный домашний канал.
func OllamaPull(ctx context.Context, model string) error {
	return OllamaPullWithProgress(ctx, model, func(p PullProgress) {
		if p.Status != "" {
			fmt.Printf("  %s\n", p.Status)
		}
	})
}

// OllamaPullWithProgress — то же, но с обратным вызовом для прогресс-бара.
func OllamaPullWithProgress(ctx context.Context, model string, report func(PullProgress)) error {
	payload, _ := json.Marshal(map[string]any{"model": model, "stream": true})

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, OllamaEndpoint+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("runtime unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("runtime http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	lastStatus := ""
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var event struct {
				Status    string `json:"status"`
				Error     string `json:"error"`
				Total     int64  `json:"total"`
				Completed int64  `json:"completed"`
			}
			if err := json.Unmarshal(line, &event); err == nil {
				if event.Error != "" {
					return fmt.Errorf("pull failed: %s", event.Error)
				}
				if report != nil && (event.Status != "" || event.Total > 0) {
					// Статус печатаем один раз, проценты — на каждое обновление.
					progress := PullProgress{Status: event.Status, Completed: event.Completed, Total: event.Total}
					if event.Status != "" && event.Status != lastStatus {
						lastStatus = event.Status
						progress.Status = event.Status
					} else {
						progress.Status = ""
					}
					report(progress)
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("pull stream: %w", readErr)
		}
	}
}
