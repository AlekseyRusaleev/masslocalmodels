package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Окно контекста и бюджет ответа, которые объявляются в настройках харнеса.
//
// Окно должно совпадать с окном узла (DefaultNumCtx): если объявить меньше,
// харнес начнёт сжимать историю раньше, чем модель упрётся в своё окно, и
// участник получит обрезанный диалог при живой памяти.
const (
	harnessContextWindow = DefaultNumCtx
	harnessMaxTokens     = 4096
)

// HarnessHome возвращает каталог настроек харнеса, если он существует.
//
// Харнес хранит конфигурацию в %APPDATA%\DeepSeekHarness\data (Windows) или
// ~/.dsh (Linux). Установщик не создаёт этот каталог: если харнеса на машине
// нет, вставлять настройки некуда.
func HarnessHome() (string, bool) {
	candidates := make([]string, 0, 3)
	if appData := os.Getenv("APPDATA"); appData != "" {
		candidates = append(candidates, filepath.Join(appData, "DeepSeekHarness", "data"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".dsh"))
	}
	if dshHome := os.Getenv("DSH_HOME"); dshHome != "" {
		candidates = append([]string{dshHome}, candidates...)
	}

	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, true
		}
	}
	return "", false
}

// HarnessInstallResult — что именно сделал установщик с конфигурацией харнеса.
type HarnessInstallResult struct {
	SettingsPath       string
	CredentialsPath    string
	SettingsChanged    bool
	CredentialsChanged bool
	Backups            []string
	// Note объясняет, почему файл не тронут, если так вышло.
	Note string
}

// InstallHarnessSettings вставляет маршрут провайдера в settings.yaml и ключ в
// .credentials.yaml.
//
// Правила осторожности:
//   - файл никогда не перезаписывается без резервной копии;
//   - если структура незнакомая (например, providers записан в потоковом виде
//     {...}), файл не трогаем и честно сообщаем, что вставить вручную;
//   - повторный запуск ничего не дублирует.
func InstallHarnessSettings(apiKey, model, baseURL string) (HarnessInstallResult, error) {
	home, ok := HarnessHome()
	if !ok {
		return HarnessInstallResult{Note: "харнес не найден: секцию нужно вставить вручную"}, nil
	}

	result := HarnessInstallResult{
		SettingsPath:    filepath.Join(home, "settings.yaml"),
		CredentialsPath: filepath.Join(home, ".credentials.yaml"),
	}

	if err := installSettingsSection(&result, model, baseURL); err != nil {
		return result, err
	}
	if err := installCredentialRef(&result, apiKey); err != nil {
		return result, err
	}
	return result, nil
}

// installSettingsSection добавляет секцию llm-pi-ai с нашим провайдером.
func installSettingsSection(result *HarnessInstallResult, model, baseURL string) error {
	path := result.SettingsPath
	original, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Файла нет — создаём документ с одной секцией.
		content := providerBlock(model, baseURL)
		if err := writeWithBackup(result, path, []byte(content), 0o600); err != nil {
			return err
		}
		result.SettingsChanged = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("прочитать настройки харнеса: %w", err)
	}

	text := string(original)
	if strings.Contains(text, "compute-network") {
		// Маршрут уже есть. Если модель та же — ничего не меняем; если сеть
		// переключилась на другую, блок переписываем, чтобы участник не
		// настраивал харнес вручную.
		if blockHasModel(text, model) {
			result.Note = "маршрут compute-network уже настроен на эту модель"
			return nil
		}
		replaced, ok := replaceProviderBlock(text, model, baseURL)
		if !ok {
			result.Note = "маршрут есть, но структуру не удалось разобрать — обновите вручную"
			return nil
		}
		if err := writeWithBackup(result, path, []byte(replaced), 0o600); err != nil {
			return err
		}
		result.SettingsChanged = true
		result.Note = "модель в настройках харнеса обновлена на " + model
		return nil
	}

	// Потоковая запись словаря провайдеров: разбирать её текстом небезопасно.
	if index := strings.Index(text, "llm-pi-ai:"); index >= 0 {
		tail := text[index:]
		if strings.Contains(tail, "{") && strings.Index(tail, "{") < strings.Index(tail, "providers:")+200 {
			result.Note = "секция llm-pi-ai записана потоково — вставьте маршрут вручную"
			return nil
		}
	}

	var updated string
	switch {
	case strings.Contains(text, "llm-pi-ai:"):
		// Секция есть, наш маршрут — нет: добавляем после строки providers.
		updated = insertProvider(text, model, baseURL)
		if updated == "" {
			result.Note = "не удалось однозначно найти providers в llm-pi-ai — вставьте вручную"
			return nil
		}
	case strings.TrimSpace(text) == "":
		updated = providerBlock(model, baseURL)
	default:
		updated = strings.TrimRight(text, "\n") + "\n\n" + providerBlock(model, baseURL)
	}

	if err := writeWithBackup(result, path, []byte(updated), 0o600); err != nil {
		return err
	}
	result.SettingsChanged = true
	return nil
}

// installCredentialRef добавляет ссылку на ключ в refs.
func installCredentialRef(result *HarnessInstallResult, apiKey string) error {
	if apiKey == "" {
		return nil
	}
	path := result.CredentialsPath
	original, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		content := fmt.Sprintf("version: 1\nrefs:\n  COMPUTE_NETWORK_API_KEY: %s\n", apiKey)
		if err := writeWithBackup(result, path, []byte(content), 0o600); err != nil {
			return err
		}
		result.CredentialsChanged = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("прочитать ключи харнеса: %w", err)
	}

	text := string(original)
	if strings.Contains(text, "COMPUTE_NETWORK_API_KEY") {
		return nil
	}

	lines := strings.Split(text, "\n")
	refsIndex := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "refs:" {
			refsIndex = i
			break
		}
	}

	var updated string
	switch {
	case refsIndex < 0:
		updated = strings.TrimRight(text, "\n") + "\nrefs:\n  COMPUTE_NETWORK_API_KEY: " + apiKey + "\n"
	case refsIndex+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[refsIndex+1]), "{"):
		result.Note = strings.TrimSpace(result.Note + "; refs записан потоково — ключ добавьте вручную")
		return nil
	default:
		// Вставляем в конец блока refs: до первой строки без отступа.
		insertAt := refsIndex + 1
		for insertAt < len(lines) {
			line := lines[insertAt]
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				insertAt++
				continue
			}
			break
		}
		updatedLines := make([]string, 0, len(lines)+1)
		updatedLines = append(updatedLines, lines[:insertAt]...)
		updatedLines = append(updatedLines, "  COMPUTE_NETWORK_API_KEY: "+apiKey)
		updatedLines = append(updatedLines, lines[insertAt:]...)
		updated = strings.Join(updatedLines, "\n")
	}

	if err := writeWithBackup(result, path, []byte(updated), 0o600); err != nil {
		return err
	}
	result.CredentialsChanged = true
	return nil
}

// insertProvider вставляет блок маршрута после строки providers: в секции llm-pi-ai.
func insertProvider(text, model, baseURL string) string {
	lines := strings.Split(text, "\n")
	sectionIndex := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "llm-pi-ai:") {
			sectionIndex = i
			break
		}
	}
	if sectionIndex < 0 {
		return ""
	}

	// providers: должен идти с отступом внутри секции.
	providersIndex := -1
	providersIndent := ""
	for i := sectionIndex + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			break // секция закончилась
		}
		if strings.TrimSpace(line) == "providers:" {
			providersIndex = i
			providersIndent = line[:len(line)-len(strings.TrimLeft(line, " "))]
			break
		}
	}
	if providersIndex < 0 {
		return ""
	}

	providerIndent := providersIndent + "  "
	fieldIndent := providerIndent + "  "
	modelIndent := fieldIndent + "  "

	block := []string{
		providerIndent + "compute-network:",
		fieldIndent + "displayName: Compute Network",
		fieldIndent + "api: openai-completions",
		fieldIndent + "baseURL: " + baseURL + "/v1",
		fieldIndent + "apiKeyEnv: COMPUTE_NETWORK_API_KEY",
		fmt.Sprintf("%sdefaultContextWindow: %d", fieldIndent, harnessContextWindow),
		fmt.Sprintf("%sdefaultMaxTokens: %d", fieldIndent, harnessMaxTokens),
		fieldIndent + "models:",
		modelIndent + "- id: \"" + model + "\"",
		modelIndent[:len(modelIndent)-2] + "  name: \"" + model + "\"",
		fmt.Sprintf("%s  contextWindow: %d", modelIndent[:len(modelIndent)-2], harnessContextWindow),
		fmt.Sprintf("%s  maxTokens: %d", modelIndent[:len(modelIndent)-2], harnessMaxTokens),
		modelIndent[:len(modelIndent)-2] + "  inputModalities: [text]",
	}

	updated := make([]string, 0, len(lines)+len(block))
	updated = append(updated, lines[:providersIndex+1]...)
	updated = append(updated, block...)
	updated = append(updated, lines[providersIndex+1:]...)
	return strings.Join(updated, "\n")
}

// providerBlock — готовая секция для файла, где llm-pi-ai ещё нет.
func providerBlock(model, baseURL string) string {
	var b strings.Builder
	b.WriteString("llm-pi-ai:\n")
	b.WriteString("  providers:\n")
	b.WriteString("    compute-network:\n")
	b.WriteString("      displayName: Compute Network\n")
	b.WriteString("      api: openai-completions\n")
	fmt.Fprintf(&b, "      baseURL: %s/v1\n", baseURL)
	b.WriteString("      apiKeyEnv: COMPUTE_NETWORK_API_KEY\n")
	fmt.Fprintf(&b, "      defaultContextWindow: %d\n", harnessContextWindow)
	fmt.Fprintf(&b, "      defaultMaxTokens: %d\n", harnessMaxTokens)
	b.WriteString("      models:\n")
	fmt.Fprintf(&b, "        - id: \"%s\"\n", model)
	fmt.Fprintf(&b, "          name: \"%s\"\n", model)
	fmt.Fprintf(&b, "          contextWindow: %d\n", harnessContextWindow)
	fmt.Fprintf(&b, "          maxTokens: %d\n", harnessMaxTokens)
	b.WriteString("          inputModalities: [text]\n")
	return b.String()
}

// writeWithBackup сохраняет копию файла и записывает новое содержимое.
func writeWithBackup(result *HarnessInstallResult, path string, content []byte, mode os.FileMode) error {
	if original, err := os.ReadFile(path); err == nil {
		backup := path + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, original, mode); err != nil {
			return fmt.Errorf("резервная копия %s: %w", backup, err)
		}
		result.Backups = append(result.Backups, backup)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return fmt.Errorf("записать %s: %w", path, err)
	}
	return nil
}

// providerBlockLines возвращает блок маршрута с заданным отступом.
func providerBlockLines(indent, model, baseURL string) []string {
	field := indent + "  "
	modelLine := field + "  "
	return []string{
		indent + "compute-network:",
		field + "displayName: Compute Network",
		field + "api: openai-completions",
		field + "baseURL: " + baseURL + "/v1",
		field + "apiKeyEnv: COMPUTE_NETWORK_API_KEY",
		fmt.Sprintf("%sdefaultContextWindow: %d", field, harnessContextWindow),
		fmt.Sprintf("%sdefaultMaxTokens: %d", field, harnessMaxTokens),
		field + "models:",
		modelLine + "- id: \"" + model + "\"",
		modelLine + "  name: \"" + model + "\"",
		fmt.Sprintf("%s  contextWindow: %d", modelLine, harnessContextWindow),
		fmt.Sprintf("%s  maxTokens: %d", modelLine, harnessMaxTokens),
		modelLine + "  inputModalities: [text]",
	}
}

// findProviderBlock находит границы блока compute-network по отступу.
// Возвращает начало, конец (не включая) и отступ.
func findProviderBlock(lines []string) (int, int, string, bool) {
	start := -1
	indent := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "compute-network:" {
			start = i
			indent = line[:len(line)-len(strings.TrimLeft(line, " "))]
			break
		}
	}
	if start < 0 {
		return 0, 0, "", false
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		currentIndent := len(line) - len(strings.TrimLeft(line, " "))
		if currentIndent <= len(indent) {
			end = i
			break
		}
	}
	return start, end, indent, true
}

// blockHasModel проверяет, что в блоке маршрута уже стоит нужная модель.
func blockHasModel(text, model string) bool {
	lines := strings.Split(text, "\n")
	start, end, _, ok := findProviderBlock(lines)
	if !ok {
		return false
	}
	wanted := `id: "` + model + `"`
	for _, line := range lines[start:end] {
		if strings.Contains(line, wanted) {
			return true
		}
	}
	return false
}

// replaceProviderBlock переписывает блок маршрута с новой моделью.
func replaceProviderBlock(text, model, baseURL string) (string, bool) {
	lines := strings.Split(text, "\n")
	start, end, indent, ok := findProviderBlock(lines)
	if !ok {
		return "", false
	}

	// Блок внутри { } — невалидный YAML: ключи блока нельзя класть в потоковую
	// запись. Скобки вокруг нашего маршрута снимаем, остальное не трогаем.
	head := lines[:start]
	tail := lines[end:]
	if len(head) > 0 && strings.TrimSpace(head[len(head)-1]) == "{" {
		head = head[:len(head)-1]
	}
	if len(tail) > 0 && strings.TrimSpace(tail[0]) == "}" {
		tail = tail[1:]
	}

	updated := make([]string, 0, len(lines)+len(providerBlockLines(indent, model, baseURL)))
	updated = append(updated, head...)
	updated = append(updated, providerBlockLines(indent, model, baseURL)...)
	updated = append(updated, tail...)
	return strings.Join(updated, "\n"), true
}
