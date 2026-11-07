package worker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Invite — приглашение подключиться к сети.
//
// Его выпускает панель одной кнопкой: внутри одноразовый токен регистрации,
// ключ участника для собственного харнеса, адрес Control Plane и модель.
// Формат — base64 от JSON, чтобы приглашение можно было переслать одной строкой
// в мессенджере и оно не ломалось переносами.
type Invite struct {
	NetworkName     string `json:"network_name,omitempty"`
	ControlURL      string `json:"control_url"`
	EnrollmentToken string `json:"enrollment_token"`
	APIKey          string `json:"api_key,omitempty"`
	Model           string `json:"model,omitempty"`
	// RevisionID — ревизия модели в каталоге: если указана, агент скачивает
	// веса по манифесту, а не просит пользователя делать ollama pull.
	RevisionID string `json:"revision_id,omitempty"`
}

// ParseInvite принимает приглашение строкой base64, путём к файлу с кодом или
// путём к JSON.
func ParseInvite(raw string) (Invite, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Invite{}, errors.New("приглашение пустое")
	}

	// Файл: либо base64-код, либо готовый JSON.
	if data, err := os.ReadFile(raw); err == nil {
		return parseInviteBytes(data)
	}

	if decoded, err := decodeBase64(raw); err == nil {
		return parseInviteBytes(decoded)
	}

	return Invite{}, errors.New("приглашение не распознано: ожидается код из панели или файл")
}

// parseInviteBytes разбирает JSON и проверяет обязательные поля.
func parseInviteBytes(data []byte) (Invite, error) {
	text := strings.TrimSpace(string(data))

	// Файл мог быть сохранён как код, а не как JSON.
	if !strings.HasPrefix(text, "{") {
		if decoded, err := decodeBase64(text); err == nil {
			text = strings.TrimSpace(string(decoded))
		}
	}

	var invite Invite
	if err := json.Unmarshal([]byte(text), &invite); err != nil {
		return Invite{}, fmt.Errorf("приглашение не разобрано: %w", err)
	}
	invite.ControlURL = strings.TrimRight(strings.TrimSpace(invite.ControlURL), "/")
	if invite.ControlURL == "" {
		return Invite{}, errors.New("в приглашении нет control_url")
	}
	if invite.EnrollmentToken == "" {
		return Invite{}, errors.New("в приглашении нет enrollment_token")
	}
	return invite, nil
}

// decodeBase64 понимает оба алфавита и отсутствие выравнивания.
func decodeBase64(raw string) ([]byte, error) {
	raw = strings.ReplaceAll(raw, "\r", "")
	raw = strings.ReplaceAll(raw, "\n", "")
	raw = strings.ReplaceAll(raw, " ", "")

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(raw); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("не base64")
}

// HarnessConfig собирает YAML-фрагмент для харнеса участника: секция
// llm-pi-ai с маршрутом на платформу.
func (i Invite) HarnessConfig(model string) string {
	if model == "" {
		model = i.Model
	}
	model = ResolveChatModel(model)
	name := i.NetworkName
	if name == "" {
		name = "Compute Network"
	}

	var b strings.Builder
	b.WriteString("# Секция для settings.yaml вашего харнеса.\n")
	b.WriteString("# Путь: %APPDATA%\\DeepSeekHarness\\data\\settings.yaml (Windows)\n")
	b.WriteString("# Применяется на следующем запросе, перезапуск не нужен.\n")
	b.WriteString("llm-pi-ai:\n")
	b.WriteString("  providers:\n")
	b.WriteString("    compute-network:\n")
	fmt.Fprintf(&b, "      displayName: %s\n", name)
	b.WriteString("      api: openai-completions\n")
	fmt.Fprintf(&b, "      baseURL: %s/v1\n", i.ControlURL)
	b.WriteString("      apiKeyEnv: COMPUTE_NETWORK_API_KEY\n")
	fmt.Fprintf(&b, "      defaultContextWindow: %d\n", DefaultNumCtx)
	b.WriteString("      defaultMaxTokens: 4096\n")
	b.WriteString("      models:\n")
	fmt.Fprintf(&b, "        - id: \"%s\"\n", model)
	fmt.Fprintf(&b, "          name: \"%s\"\n", model)
	fmt.Fprintf(&b, "          contextWindow: %d\n", DefaultNumCtx)
	b.WriteString("          maxTokens: 4096\n")
	b.WriteString("          inputModalities: [text]\n")
	b.WriteString("\n")
	b.WriteString("# Ключ участника — в %APPDATA%\\DeepSeekHarness\\data\\.credentials.yaml:\n")
	b.WriteString("# refs:\n")
	if i.APIKey != "" {
		fmt.Fprintf(&b, "#   COMPUTE_NETWORK_API_KEY: %s\n", i.APIKey)
	} else {
		b.WriteString("#   COMPUTE_NETWORK_API_KEY: <ключ из панели>\n")
	}
	return b.String()
}
