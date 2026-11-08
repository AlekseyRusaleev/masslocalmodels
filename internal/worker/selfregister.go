package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/private-compute/network/internal/workercfg"
)

// DefaultControlURL — адрес платформы, с которого программа начинает работу.
// Участнику не нужно его знать: он либо прописан в сборке, либо указан явно.
const DefaultControlURL = "https://api.nshno.tech"

// SelfRegisterOptions — параметры самозаписи узла.
type SelfRegisterOptions struct {
	// ControlURL — адрес платформы, если участник его знает. Пусто — берём
	// адрес по умолчанию: программа должна работать «из коробки».
	ControlURL string
	// JoinCode — общий код сети, если владелец его включил.
	JoinCode string
	// Name — имя узла. Пусто — имя компьютера.
	Name string
	// ConfigPath — куда сохранить конфигурацию.
	ConfigPath string
	// HarnessPath — необязательный путь для настроек харнеса.
	HarnessPath string
	// PullModel — скачать модель, если её нет локально.
	PullModel bool
	// AskOllama спрашивает, можно ли поставить Ollama, если её нет.
	AskOllama func() bool
	Log       func(string)
	Progress  func(percent int, status string)
	// OnCredentials отдаёт данные для харнеса сразу после регистрации,
	// не дожидаясь скачивания модели.
	OnCredentials func(baseURL, model, apiKey string)
}

// SelfRegisterResult — что получилось.
type SelfRegisterResult struct {
	Config      workercfg.Config
	Model       string
	WorkerID    string
	TrustClass  string
	APIKey      string
	HarnessFile string
	// ConnectionFile — памятка с параметрами подключения для любого клиента.
	ConnectionFile string
	Pulled         bool
}

// SelfRegister регистрирует узел без приглашения.
//
// Путь участника не должен зависеть от владельца сети: программа спрашивает у
// платформы всё, что нужно (секрет устройства, ключ для харнеса, какую модель
// скачивать), и сохраняет это локально. Приглашение остаётся для случаев, когда
// владелец хочет выдать конкретному человеку закрытый доступ.
func SelfRegister(ctx context.Context, opts SelfRegisterOptions) (SelfRegisterResult, error) {
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

	control := strings.TrimRight(strings.TrimSpace(opts.ControlURL), "/")
	if control == "" {
		control = DefaultControlURL
	}
	if !strings.HasPrefix(control, "http") {
		control = "https://" + control
	}

	if opts.PullModel {
		progress(3, "проверяю Ollama")
		if err := EnsureOllama(ctx, EnsureOllamaOptions{
			Ask:      opts.AskOllama,
			Log:      opts.Log,
			Progress: opts.Progress,
		}); err != nil {
			if errors.Is(err, ErrOllamaDeclined) {
				return SelfRegisterResult{}, fmt.Errorf("без Ollama модель не скачается: установка отклонена")
			}
			return SelfRegisterResult{}, err
		}
		progress(4, "проверяю llama.cpp")
		if dir, err := EnsureLlama(ctx, EnsureLlamaOptions{Log: opts.Log, Progress: opts.Progress}); err != nil {
			logf("llama.cpp не установлена, разделение слоёв недоступно: %v", err)
		} else {
			logf("llama.cpp: %s", dir)
		}
	}

	progress(5, "определяю железо")
	info, err := Discover(ctx)
	if err != nil {
		return SelfRegisterResult{}, fmt.Errorf("не удалось определить железо: %w", err)
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = info.Hostname
	}

	progress(25, "регистрирую узел на платформе")
	payload, _ := json.Marshal(map[string]any{
		"name":          name,
		"hostname":      info.Hostname,
		"os":            info.OS,
		"agent_version": AgentVersion,
		"join_code":     strings.TrimSpace(opts.JoinCode),
		"hardware": map[string]any{
			"cpu_model":      info.CPUModel,
			"cpu_cores":      info.CPUCores,
			"ram_bytes":      info.RAMBytes,
			"gpus":           info.GPUs,
			"disks":          info.Disks,
			"cuda_version":   info.CUDAVer,
			"driver_version": info.DriverVer,
		},
	})

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, control+"/v1/nodes/register", bytes.NewReader(payload))
	if err != nil {
		return SelfRegisterResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "compute-network-node/"+AgentVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SelfRegisterResult{}, fmt.Errorf("платформа недоступна: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 300 {
		return SelfRegisterResult{}, fmt.Errorf("регистрация отклонена: %s", serverMessage(raw))
	}

	var body struct {
		WorkerID    string `json:"worker_id"`
		Name        string `json:"name"`
		TrustClass  string `json:"trust_class"`
		DeviceToken string `json:"device_token"`
		APIKey      string `json:"api_key"`
		ControlURL  string `json:"control_url"`
		Model       string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return SelfRegisterResult{}, fmt.Errorf("непонятный ответ платформы: %w", err)
	}
	if body.DeviceToken == "" {
		return SelfRegisterResult{}, fmt.Errorf("платформа не выдала секрет устройства")
	}
	body.Model = ResolveChatModel(body.Model)
	logf("модель для этого узла: %s", body.Model)
	if opts.OnCredentials != nil && strings.TrimSpace(body.APIKey) != "" {
		opts.OnCredentials(strings.TrimRight(control, "/")+"/v1", body.Model, body.APIKey)
	}

	result := SelfRegisterResult{WorkerID: body.WorkerID, TrustClass: body.TrustClass, APIKey: body.APIKey}

	progress(45, "сохраняю конфигурацию")
	cfg := workercfg.Config{
		ControlURL:       control,
		WorkerName:       body.Name,
		DeviceToken:      body.DeviceToken,
		HeartbeatSeconds: 30,
		Model:            body.Model,
		NodeKey:          nodeKey(),
		EnrolledAt:       time.Now().UTC(),
	}
	if err := workercfg.Save(opts.ConfigPath, cfg); err != nil {
		return SelfRegisterResult{}, fmt.Errorf("не удалось сохранить конфигурацию: %w", err)
	}
	result.Config = cfg
	result.Model = body.Model
	logf("узел %s зарегистрирован, секрет устройства: %s", cfg.WorkerName, workercfg.SecretProtection())

	// Настройки харнеса: участник не должен вставлять их руками.
	if body.APIKey != "" && body.Model != "" {
		progress(60, "настраиваю харнес")
		if opts.HarnessPath != "" {
			if err := os.MkdirAll(filepath.Dir(opts.HarnessPath), 0o755); err == nil {
				snippet := Invite{ControlURL: control}.HarnessConfig(body.Model)
				if err := os.WriteFile(opts.HarnessPath, []byte(snippet), 0o644); err == nil {
					result.HarnessFile = opts.HarnessPath
					logf("фрагмент провайдера харнеса: %s", opts.HarnessPath)
				}
			}
		}
		install, err := InstallHarnessSettings(body.APIKey, body.Model, control)
		if err != nil {
			logf("настройки харнеса не установлены: %v", err)
		} else {
			if install.SettingsPath != "" {
				result.HarnessFile = install.SettingsPath
			}
			if install.Note != "" {
				logf("харнес: %s", install.Note)
			} else if install.SettingsChanged || install.CredentialsChanged {
				logf("харнес: провайдер compute-network записан, base URL %s/v1", control)
			}
		}
	}

	if body.APIKey != "" && body.Model != "" {
		connectionPath := writeConnectionNote(opts.ConfigPath, control, body.Model, body.APIKey)
		if connectionPath != "" {
			result.ConnectionFile = connectionPath
			logf("параметры подключения: %s", connectionPath)
		}
		logf("для любого OpenAI-совместимого клиента: base URL %s/v1, модель %s, ключ %s",
			control, body.Model, MaskToken(body.APIKey))
	}

	if opts.PullModel && body.Model != "" {
		progress(75, "проверяю модель")
		if has, err := hasModel(ctx, body.Model); err == nil && !has {
			logf("скачиваю модель %s", body.Model)
			if err := OllamaPullWithProgress(ctx, body.Model, func(p PullProgress) {
				status := p.Status
				if p.Total > 0 {
					status = fmt.Sprintf("%s %d%%", p.Status, p.Completed*100/p.Total)
				}
				progress(75, status)
			}); err != nil {
				return result, fmt.Errorf("модель не скачана: %w", err)
			}
			result.Pulled = true
		}
		if gptOSS20(body.Model) {
			progress(0, "скачиваю голову черновика")
			logf("скачиваю голову черновика")
			draft, err := FetchDraftHead(ctx, func(done, total int64) {
				if total <= 0 {
					return
				}
				pct := done * 100 / total
				if pct > 99 {
					pct = 99
				}
				progress(int(pct), fmt.Sprintf("голова черновика: %d%%", pct))
			})
			if err != nil {
				logf("голова черновика не скачана: %v", err)
			} else if cfg, loadErr := workercfg.Load(opts.ConfigPath); loadErr == nil {
				cfg.DraftGGUFPath = draft
				if saveErr := workercfg.Save(opts.ConfigPath, cfg); saveErr != nil {
					logf("путь головы не записан: %v", saveErr)
				} else {
					logf("голова черновика: %s", draft)
				}
			}
		}
	}

	progress(100, "готово")
	return result, nil
}

// writeConnectionNote пишет рядом с конфигом памятку подключения.
//
// Харнес DeepSeek мы умеем настраивать сами, а OpenCode, Cline, Continue,
// aider и обычные SDK — нет: у каждого свой формат и своё место конфигурации.
// Поэтому участник получает файл с базовым URL, моделью и ключом плюс примеры
// для популярных клиентов.
func writeConnectionNote(cfgPath, control, model, apiKey string) string {
	dir := filepath.Dir(cfgPath)
	if cfgPath == "" {
		dir = "."
	}
	path := filepath.Join(dir, "connection.txt")
	text := fmt.Sprintf(`Подключение к сети Compute
==========================

Базовый URL:  %[1]s/v1
Модель:       %[2]s
Ключ API:     %[3]s

Кастомный провайдер (OpenAI Compatible):
  Тип API:      OpenAI Compatible
  Base URL:     %[1]s/v1
  Ключ API:     %[3]s
  Модель:       %[2]s
  Заголовок:    Authorization: Bearer <ключ>

Это стандартный OpenAI-совместимый API: подойдёт любой клиент, который умеет
указывать свой base URL и ключ.

Проверка из терминала:

  curl %[1]s/v1/models -H "Authorization: Bearer %[3]s"

  curl %[1]s/v1/chat/completions ^
    -H "Content-Type: application/json" ^
    -H "Authorization: Bearer %[3]s" ^
    -d "{\"model\":\"%[2]s\",\"messages\":[{\"role\":\"user\",\"content\":\"привет\"}]}"

Клиенты (OpenAI Compatible / Custom provider):
  DeepSeek Harness — настроен автоматически (%[4]s).
  Cline / Roo Code  — Provider: OpenAI Compatible, Base URL и ключ выше.
  Continue          — models: provider: openai, apiBase: %[1]s/v1, apiKey.
  OpenCode          — provider: openai-compatible, baseURL: %[1]s/v1, model %[2]s.
  aider             — OPENAI_API_BASE=%[1]s/v1, OPENAI_API_KEY=<ключ>, --model openai/%[2]s.
  Python/Node SDK   — base_url="%[1]s/v1", api_key="<ключ>".

Чего нет: протокола Anthropic (/v1/messages), поэтому Claude Code без
прокси-переходника не подключить. Эмбеддинги — POST /v1/embeddings.
`,
		control, model, apiKey, harnessNotePath(cfgPath))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ""
	}
	return path
}

// harnessNotePath — где лежат настройки харнеса, если они установлены.
func harnessNotePath(cfgPath string) string {
	if home, ok := HarnessHome(); ok {
		return filepath.Join(home, "settings.yaml")
	}
	return "не найден"
}

// hasModel сообщает, есть ли модель в локальном рантайме.
func hasModel(ctx context.Context, model string) (bool, error) {
	models, err := OllamaModels(ctx)
	if err != nil {
		return false, err
	}
	for _, name := range models {
		if strings.EqualFold(name, model) {
			return true, nil
		}
	}
	return false, nil
}

// serverMessage достаёт текст ошибки из ответа сервера.
func serverMessage(raw []byte) string {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		if body.Error.Code != "" {
			return body.Error.Code + ": " + body.Error.Message
		}
		return body.Error.Message
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
