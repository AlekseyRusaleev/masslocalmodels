package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ChatRequest — тело запроса /v1/chat/completions в объёме, который платформа
// понимает и передаёт узлу. Поля совпадают с OpenAI, чтобы харнес и SDK могли
// обращаться к платформе как к обычному провайдеру.
type ChatRequest struct {
	Model       string          `json:"model"`
	Stream      bool            `json:"stream"`
	Messages    []JobMessage    `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Seed        *int            `json:"seed,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
}

// JobMessage — сообщение в том виде, в каком его получает узел.
type JobMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	ToolCalls []OllamaCall `json:"tool_calls,omitempty"`
}

// OllamaCall — вызов инструмента в форме рантайма: arguments — объект,
// а не строка JSON (у OpenAI наоборот).
type OllamaCall struct {
	Function OllamaFunction `json:"function"`
}

// OllamaFunction — имя и аргументы вызова.
type OllamaFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// incomingRequest — то, что реально приходит по проводу: content может быть
// строкой или массивом частей, аргументы инструментов — строкой.
type incomingRequest struct {
	Model       string            `json:"model"`
	Stream      bool              `json:"stream"`
	Messages    []incomingMessage `json:"messages"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature *float64          `json:"temperature"`
	TopP        *float64          `json:"top_p"`
	Seed        *int              `json:"seed"`
	Stop        json.RawMessage   `json:"stop"`
	Tools       json.RawMessage   `json:"tools"`
	ToolChoice  json.RawMessage   `json:"tool_choice"`
}

type incomingMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []incomingCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type incomingCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ParseChatRequest разбирает тело запроса и приводит сообщения к виду рантайма.
func ParseChatRequest(raw []byte) (ChatRequest, error) {
	var incoming incomingRequest
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return ChatRequest{}, errors.New("invalid JSON body: " + err.Error())
	}
	if len(incoming.Messages) == 0 {
		return ChatRequest{}, errors.New("messages are required")
	}

	messages := make([]JobMessage, 0, len(incoming.Messages))
	for index, message := range incoming.Messages {
		text, err := flattenContent(message.Content)
		if err != nil {
			return ChatRequest{}, fmt.Errorf("messages[%d]: %w", index, err)
		}
		messages = append(messages, JobMessage{
			Role:      normalizeRole(message.Role),
			Content:   text,
			ToolCalls: normalizeToolCalls(message.ToolCalls),
		})
	}

	return ChatRequest{
		Model:       incoming.Model,
		Stream:      incoming.Stream,
		Messages:    messages,
		MaxTokens:   incoming.MaxTokens,
		Temperature: incoming.Temperature,
		TopP:        incoming.TopP,
		Seed:        incoming.Seed,
		Stop:        incoming.Stop,
		Tools:       incoming.Tools,
		ToolChoice:  incoming.ToolChoice,
	}, nil
}

// flattenContent превращает content в строку: строка как есть, массив частей —
// склейка текстовых частей. Изображения не поддерживаются этим классом моделей.
func flattenContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", errors.New("content must be a string or an array of parts")
	}

	var builder strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text", "":
			builder.WriteString(part.Text)
		case "image_url", "input_image":
			return "", errors.New("image input is not supported by this model yet")
		default:
			// Неизвестные части пропускаем: лучше ответить без них, чем упасть.
		}
	}
	return builder.String(), nil
}

// normalizeRole приводит роли к тем, что понимает рантайм.
func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "developer":
		return "system"
	case "user", "assistant", "system", "tool":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "user"
	}
}

// normalizeToolCalls переводит аргументы из строки JSON в объект: рантайм
// принимает объект, а клиенты OpenAI присылают строку.
func normalizeToolCalls(raw []incomingCall) []OllamaCall {
	if len(raw) == 0 {
		return nil
	}
	out := make([]OllamaCall, 0, len(raw))
	for _, call := range raw {
		arguments := json.RawMessage(`{}`)
		if trimmed := strings.TrimSpace(call.Function.Arguments); trimmed != "" && json.Valid([]byte(trimmed)) {
			arguments = json.RawMessage(trimmed)
		}
		out = append(out, OllamaCall{Function: OllamaFunction{
			Name:      call.Function.Name,
			Arguments: arguments,
		}})
	}
	return out
}
