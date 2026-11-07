package worker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// harnessHomeForTest создаёт временный каталог харнеса и подменяет APPDATA.
func harnessHomeForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "DeepSeekHarness", "data")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("создать каталог харнеса: %v", err)
	}
	t.Setenv("APPDATA", root)
	t.Setenv("DSH_HOME", "")
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	return home
}

func TestInstallHarnessSettingsCreatesFiles(t *testing.T) {
	home := harnessHomeForTest(t)

	result, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("установка настроек: %v", err)
	}
	if !result.SettingsChanged || !result.CredentialsChanged {
		t.Fatalf("ожидались обе правки, получено %+v", result)
	}

	settings, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil {
		t.Fatalf("прочитать настройки: %v", err)
	}
	text := string(settings)
	for _, want := range []string{"llm-pi-ai:", "compute-network:", "apiKeyEnv: COMPUTE_NETWORK_API_KEY", `id: "qwen3:8b"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("в настройках нет %q:\n%s", want, text)
		}
	}

	credentials, err := os.ReadFile(filepath.Join(home, ".credentials.yaml"))
	if err != nil {
		t.Fatalf("прочитать ключи: %v", err)
	}
	if !strings.Contains(string(credentials), "COMPUTE_NETWORK_API_KEY: dai_test.secret") {
		t.Fatalf("ключ не записан:\n%s", credentials)
	}
}

// Окно контекста в настройках харнеса должно совпадать с окном узла: иначе
// харнес сжимает историю раньше, чем модель упирается в своё окно, и участник
// видит обрезанный диалог, хотя память ещё есть.
func TestInstallHarnessSettingsUsesNodeContextWindow(t *testing.T) {
	home := harnessHomeForTest(t)

	if _, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech"); err != nil {
		t.Fatalf("установка настроек: %v", err)
	}
	settings, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil {
		t.Fatalf("прочитать настройки: %v", err)
	}
	window := strconv.Itoa(DefaultNumCtx)
	text := string(settings)
	if !strings.Contains(text, "defaultContextWindow: "+window) {
		t.Fatalf("defaultContextWindow не равен окну узла (%s):\n%s", window, text)
	}
	if !strings.Contains(text, "contextWindow: "+window) {
		t.Fatalf("contextWindow модели не равен окну узла (%s):\n%s", window, text)
	}
	// Повторная установка не должна менять окно обратно.
	if _, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech"); err != nil {
		t.Fatalf("повторная установка: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil {
		t.Fatalf("прочитать настройки: %v", err)
	}
	if !strings.Contains(string(again), "contextWindow: "+window) {
		t.Fatalf("после повторной установки окно изменилось:\n%s", string(again))
	}
}

func TestInstallHarnessSettingsInsertsIntoExistingSection(t *testing.T) {
	home := harnessHomeForTest(t)
	settingsPath := filepath.Join(home, "settings.yaml")
	original := `ui-theme:
  preference: dark
llm-pi-ai:
  providers:
    ds:
      displayName: ds
      api: openai-completions
      baseURL: https://seekai.cc/v1
      models:
        - id: deepseek-v4.1-flash
agent-default-model:
  provider: deepseek-official
`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}

	result, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("установка настроек: %v", err)
	}
	if !result.SettingsChanged {
		t.Fatalf("ожидалась правка настроек, получено: %+v", result)
	}
	if len(result.Backups) == 0 {
		t.Fatal("не создана резервная копия")
	}

	updated, _ := os.ReadFile(settingsPath)
	text := string(updated)
	if !strings.Contains(text, "compute-network:") {
		t.Fatalf("маршрут не вставлен:\n%s", text)
	}
	// Наш блок должен оказаться внутри llm-pi-ai, до следующей секции.
	section := text[strings.Index(text, "llm-pi-ai:"):strings.Index(text, "agent-default-model:")]
	if !strings.Contains(section, "compute-network:") {
		t.Fatalf("маршрут вне секции llm-pi-ai:\n%s", section)
	}
	// Чужая настройка должна остаться на месте.
	if !strings.Contains(text, "preference: dark") || !strings.Contains(text, "deepseek-v4.1-flash") {
		t.Fatalf("потеряны существующие настройки:\n%s", text)
	}
}

func TestInstallHarnessSettingsSkipsFlowStyle(t *testing.T) {
	home := harnessHomeForTest(t)
	settingsPath := filepath.Join(home, "settings.yaml")
	original := `llm-pi-ai:
  providers:
    {
      ds:
        {
          displayName: ds,
          api: openai-completions,
          baseURL: https://seekai.cc/v1,
          models: [ { id: deepseek-v4.1-flash } ]
        }
    }
`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}

	result, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("установка настроек: %v", err)
	}
	if result.SettingsChanged {
		t.Fatal("потоковый файл нельзя менять автоматически")
	}
	if result.Note == "" {
		t.Fatal("ожидалось пояснение для ручной вставки")
	}

	after, _ := os.ReadFile(settingsPath)
	if string(after) != original {
		t.Fatal("файл изменён, хотя не должен был")
	}
}

func TestInstallHarnessSettingsUnwrapsFlowBraces(t *testing.T) {
	home := harnessHomeForTest(t)
	settingsPath := filepath.Join(home, "settings.yaml")
	original := `ui-theme:
  preference: dark
llm-pi-ai:
  providers:
    {
      compute-network:
        displayName: Compute Network
        api: openai-completions
        baseURL: https://api.nshno.tech/v1
        apiKeyEnv: COMPUTE_NETWORK_API_KEY
        models:
          - id: "qwen3:8b"
            name: "qwen3:8b"
    }
`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}

	result, err := InstallHarnessSettings("dai_test.secret", "gpt-oss:20b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("установка настроек: %v", err)
	}
	if !result.SettingsChanged {
		t.Fatal("сломанные скобки нужно снять")
	}
	text, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("прочитать: %v", err)
	}
	got := string(text)
	if strings.Contains(got, "{") || strings.Contains(got, "}") {
		t.Fatalf("скобки остались:\n%s", got)
	}
	if !strings.Contains(got, `id: "gpt-oss:20b"`) {
		t.Fatalf("модель не обновлена:\n%s", got)
	}
	if !strings.Contains(got, "preference: dark") {
		t.Fatal("чужая секция пропала")
	}
}

func TestInstallHarnessSettingsIdempotent(t *testing.T) {
	home := harnessHomeForTest(t)

	if _, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech"); err != nil {
		t.Fatalf("первая установка: %v", err)
	}
	result, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("повторная установка: %v", err)
	}
	if result.SettingsChanged || result.CredentialsChanged {
		t.Fatalf("повторный запуск не должен ничего менять: %+v", result)
	}

	settings, _ := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if count := strings.Count(string(settings), "compute-network:"); count != 1 {
		t.Fatalf("маршрут продублирован (%d раз):\n%s", count, settings)
	}
}

func TestInstallHarnessSettingsCredentialsBlock(t *testing.T) {
	home := harnessHomeForTest(t)
	path := filepath.Join(home, ".credentials.yaml")
	original := `version: 1
records:
  client-connection/browser-session:
    kind: grant
refs:
  DEEPSEEK_API_KEY: sk-123
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}

	if _, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech"); err != nil {
		t.Fatalf("установка: %v", err)
	}
	updated, _ := os.ReadFile(path)
	text := string(updated)
	if !strings.Contains(text, "  COMPUTE_NETWORK_API_KEY: dai_test.secret") {
		t.Fatalf("ключ не добавлен в refs:\n%s", text)
	}
	if !strings.Contains(text, "DEEPSEEK_API_KEY: sk-123") {
		t.Fatalf("потерян существующий ключ:\n%s", text)
	}
	// Ключ должен оказаться внутри refs, а не после records.
	refsPart := text[strings.Index(text, "refs:"):]
	if strings.Contains(refsPart, "kind: grant") {
		t.Fatalf("ключ вставлен не в тот блок:\n%s", text)
	}
}

func TestInstallHarnessSettingsUpdatesModel(t *testing.T) {
	home := harnessHomeForTest(t)

	if _, err := InstallHarnessSettings("dai_test.secret", "qwen3:8b", "https://api.nshno.tech"); err != nil {
		t.Fatalf("первая установка: %v", err)
	}

	// Сеть переключилась на другую модель — маршрут должен обновиться сам.
	result, err := InstallHarnessSettings("dai_test.secret", "qwen3:32b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("обновление: %v", err)
	}
	if !result.SettingsChanged {
		t.Fatalf("ожидалось обновление модели, получено: %+v", result)
	}

	settings, _ := os.ReadFile(filepath.Join(home, "settings.yaml"))
	text := string(settings)
	if !strings.Contains(text, `id: "qwen3:32b"`) {
		t.Fatalf("модель не обновлена:\n%s", text)
	}
	if strings.Contains(text, `id: "qwen3:8b"`) {
		t.Fatalf("старая модель осталась в файле:\n%s", text)
	}
	if count := strings.Count(text, "compute-network:"); count != 1 {
		t.Fatalf("блок продублирован (%d):\n%s", count, text)
	}
	if len(result.Backups) == 0 {
		t.Fatal("не создана резервная копия перед обновлением")
	}

	// Повторный вызов с той же моделью ничего не меняет.
	again, err := InstallHarnessSettings("dai_test.secret", "qwen3:32b", "https://api.nshno.tech")
	if err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if again.SettingsChanged {
		t.Fatalf("повторный вызов не должен менять файл: %+v", again)
	}
}
