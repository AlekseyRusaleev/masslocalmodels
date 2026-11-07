// Package workercfg — локальная конфигурация Worker Agent.
//
// Файл содержит секрет устройства и поэтому создаётся с правами 0600.
// Путь по умолчанию: %ProgramData%\ComputeNetwork\agent.json (Windows)
// и /etc/compute-network/agent.json (Linux).
package workercfg

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config — сохраняемые между запусками параметры агента.
type Config struct {
	ControlURL       string    `json:"control_url"`
	WorkerName       string    `json:"worker_name"`
	DeviceToken      string    `json:"device_token,omitempty"`
	DeviceTokenEnc   string    `json:"device_token_enc,omitempty"`
	HeartbeatSeconds int       `json:"heartbeat_seconds"`
	Paused           bool      `json:"paused"`
	EnrolledAt       time.Time `json:"enrolled_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`

	// Model — модель, которую этот узел обслуживает в чат-заданиях, если её не
	// указали флагом. Пустая строка означает «узел не обслуживает задания».
	Model string `json:"model,omitempty"`
	// EmbeddingModel — модель эмбеддингов, которую узел обслуживает отдельно от
	// чат-модели. Пустая строка — узел сам подберёт подходящую локальную модель
	// (имя содержит «embed») и объявит её платформе.
	EmbeddingModel string `json:"embedding_model,omitempty"`

	// NumCtx — окно контекста для заданий. 0 означает значение по умолчанию
	// (16384). Больше ставить имеет смысл только на картах с запасом VRAM:
	// на 8 GB выше 16384 модель частично уезжает в CPU и скорость падает
	// втрое (измерено: 70.6 tok/s на 16K против 20 tok/s на 40K).
	NumCtx int `json:"num_ctx,omitempty"`
	// NodeKey — короткий идентификатор узла для человека. Не секрет.
	NodeKey string `json:"node_key,omitempty"`

	// DraftGGUFPath — голова черновика (EAGLE-3) для текущей модели.
	// Пусто, если головы ещё нет. Не включает шард-режим само по себе.
	DraftGGUFPath string `json:"draft_gguf_path,omitempty"`

	// Mesh — шардирование модели между узлами через llama.cpp RPC. Пустое
	// значение означает обычный режим: узел обслуживает модель целиком через
	// Ollama. Заполненное — узел участвует в шард-группе (ADR 0008).
	Mesh *Mesh `json:"mesh,omitempty"`
}

// Mesh — параметры участия узла в шардированной модели.
//
// Роли:
//   - "coordinator" — держит llama-server и обслуживает запросы клиентов;
//     его слои плюс слои соисполнителей образуют целую модель;
//   - "compute" — держит только ggml-rpc-server: слои и активации, без API.
//
// Адреса соисполнителей координатор получает в Peers. Порт RPC никогда не
// слушает публичный интерфейс: по ADR 0008 он доступен только через туннель,
// поэтому по умолчанию ListenHost = 127.0.0.1.
type Mesh struct {
	Role string `json:"role"`
	// Model — имя модели, под которым шард-группа видна платформе и клиентам.
	Model string `json:"model,omitempty"`
	// GGUFPath — файл весов, который читает координатор. Каждая стадия обязана
	// иметь свой локальный файл: передавать гигабайты по сети нельзя (ADR 0008 §5.2).
	GGUFPath string `json:"gguf_path,omitempty"`
	// DraftGGUFPath — голова EAGLE-3 этой модели. Координатор передаёт её
	// llama-server как -md. Пусто — проверка по одному токену.
	DraftGGUFPath string `json:"draft_gguf_path,omitempty"`
	// SpecType — вид черновика для llama-server, например draft-eagle3.
	SpecType string `json:"spec_type,omitempty"`
	// NCPUMoE — сколько слоёв экспертов оставить в оперативной памяти.
	// Для GPT-OSS-20B на карте 8 ГБ измеренное значение — 22.
	NCPUMoE int `json:"n_cpu_moe,omitempty"`
	// LlamaDir — каталог с llama-server и ggml-rpc-server.
	LlamaDir string `json:"llama_dir,omitempty"`
	// Layers — диапазон слоёв стадии, например "0-17". Нужен для отчёта и
	// сверки с топологией на Control Plane.
	Layers string `json:"layers,omitempty"`
	// Peers — RPC-адреса соисполнителей (host:port) для координатора.
	Peers []string `json:"peers,omitempty"`
	// Split — доли модели по устройствам в порядке llama.cpp (--tensor-split).
	Split []float32 `json:"split,omitempty"`
	// Ctx — окно контекста шард-группы.
	Ctx int `json:"ctx,omitempty"`
	// ServerPort — порт llama-server (по умолчанию 18081).
	ServerPort int `json:"server_port,omitempty"`
	// RPCPort — порт ggml-rpc-server (по умолчанию 50052).
	RPCPort int `json:"rpc_port,omitempty"`
	// ListenHost — интерфейс RPC-сервера. Не loopback разрешается только в
	// доверенной сети: у протокола нет ни TLS, ни авторизации.
	ListenHost string `json:"listen_host,omitempty"`
	// Threads — потоки CPU-стадии.
	Threads int `json:"threads,omitempty"`
	// Cache — локальный кэш тензоров на стадии (флаг -c ggml-rpc-server).
	Cache bool `json:"cache,omitempty"`
	// Tunnel — соединяться с соисполнителями через платформу, а не напрямую.
	// Нужен там, где прямого адреса нет: домашний NAT, мобильный интернет,
	// разные города. Порт RPC при этом остаётся на loopback.
	Tunnel bool `json:"tunnel,omitempty"`
	// PeerWorkers — узлы-соисполнители по идентификаторам: туннель строится
	// именно к узлу, а не к адресу, потому что адреса у него может не быть.
	PeerWorkers []string `json:"peer_workers,omitempty"`
	// TopologyID и SelfWorkerID нужны туннелю: платформа сводит стороны
	// строго по паре «топология + стадия».
	TopologyID   string `json:"topology_id,omitempty"`
	SelfWorkerID string `json:"self_worker_id,omitempty"`
	// CoordinatorID нужен стадии: она отправляет свои байты в очередь
	// координатора и должна знать, кто это.
	CoordinatorID string `json:"coordinator_id,omitempty"`
}

// DefaultPath возвращает путь к конфигурации для текущей ОС.
func DefaultPath() string {
	if runtime.GOOS == "windows" {
		if programData := os.Getenv("ProgramData"); programData != "" {
			return filepath.Join(programData, "ComputeNetwork", "agent.json")
		}
		return filepath.Join(`C:\ProgramData`, "ComputeNetwork", "agent.json")
	}
	return "/etc/compute-network/agent.json"
}

// DefaultModelDir возвращает каталог весов, куда этот процесс реально может писать.
//
// Сначала общий каталог ProgramData. Если Windows его закрыла, берётся
// каталог пользователя: иначе голова черновика не скачивается.
func DefaultModelDir() string {
	if runtime.GOOS != "windows" {
		return "/var/lib/compute-network/models"
	}
	var dirs []string
	if programData := os.Getenv("ProgramData"); programData != "" {
		dirs = append(dirs, filepath.Join(programData, "ComputeNetwork", "models"))
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "ComputeNetwork", "models"))
	}
	if len(dirs) == 0 {
		return filepath.Join(`C:\ProgramData`, "ComputeNetwork", "models")
	}
	return firstWritableDir(dirs)
}

func firstWritableDir(dirs []string) string {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		probe := filepath.Join(dir, ".write-probe")
		file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		_ = file.Close()
		_ = os.Remove(probe)
		return dir
	}
	return dirs[len(dirs)-1]
}

// Load читает конфигурацию из файла.
//
// Секрет устройства лежит в файле зашифрованным (DPAPI на Windows), если
// платформа это умеет; открытым текстом — только как запасной вариант, и тогда
// защитой остаются права файла 0600.
func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("agent is not enrolled: %s not found", path)
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.DeviceTokenEnc != "" {
		encrypted, err := base64.StdEncoding.DecodeString(cfg.DeviceTokenEnc)
		if err != nil {
			return Config{}, fmt.Errorf("decode protected token: %w", err)
		}
		token, err := UnprotectSecret(encrypted)
		if err != nil {
			return Config{}, fmt.Errorf("unprotect device token: %w", err)
		}
		cfg.DeviceToken = string(token)
	}
	if cfg.HeartbeatSeconds <= 0 {
		cfg.HeartbeatSeconds = 30
	}
	return cfg, nil
}

// Save атомарно записывает конфигурацию с правами 0600.
//
// Секрет устройства шифруется доступными средствами платформы и открытым
// текстом на диск не попадает.
func Save(path string, cfg Config) error {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	cfg.UpdatedAt = time.Now().UTC()

	if cfg.DeviceToken != "" {
		if encrypted, err := ProtectSecret([]byte(cfg.DeviceToken)); err == nil {
			cfg.DeviceTokenEnc = base64.StdEncoding.EncodeToString(encrypted)
			cfg.DeviceToken = ""
		}
		// Если платформа не умеет защищать секрет, оставляем его открытым:
		// конфиг всё равно доступен только владельцу файла.
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// На Windows os.Rename перезаписывает существующий файл, на Linux тоже.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	// Права 0600 не гарантируются на Windows, но попытка стоит дешево.
	_ = os.Chmod(path, 0o600)
	return nil
}
