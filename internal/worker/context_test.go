package worker

import (
	"strings"
	"testing"

	"github.com/private-compute/network/internal/workercfg"
)

// workercfgForTest — конфигурация узла с заданным окном контекста.
func workercfgForTest(numCtx int) workercfg.Config {
	return workercfg.Config{NumCtx: numCtx}
}

// Повторяющийся русский текст — как в реальном длинном диалоге.
func longMessage(chars int) ChatMessage {
	sentence := "Правило резервирования: узел не должен зависеть от одного канала связи. "
	return ChatMessage{
		Role:    "user",
		Content: strings.Repeat(sentence, chars/len([]rune(sentence))+1),
	}
}

func TestMinPromptTokensIsLowerBound(t *testing.T) {
	// 6000 символов при самой плотной токенизации — не меньше 1000 токенов.
	message := longMessage(6000)
	tokens := MinPromptTokens(ChatRequest{Messages: []ChatMessage{message}})
	chars := len([]rune(message.Content))
	if tokens != chars/charsPerTokenMax {
		t.Fatalf("ожидалось %d, получено %d", chars/charsPerTokenMax, tokens)
	}
	if tokens > len([]rune(message.Content))/2 {
		t.Fatalf("оценка %d не может быть больше реального числа токенов для русского текста", tokens)
	}
}

func TestMinPromptTokensCountsTools(t *testing.T) {
	tools := []byte(`{"type":"function","function":{"name":"read_file","description":"читает файл с диска"}}`)
	withTools := MinPromptTokens(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: strings.Repeat("а", 600)}},
		Tools:    tools,
	})
	withoutTools := MinPromptTokens(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: strings.Repeat("а", 600)}},
	})
	if withTools <= withoutTools {
		t.Fatalf("описания инструментов не учтены: %d против %d", withTools, withoutTools)
	}
}

func TestPromptTruncatedDetectsDroppedHistory(t *testing.T) {
	// 90 000 символов не могут уложиться в 8194 токена: рантайм обрезал вход.
	dropped := ChatRequest{Messages: []ChatMessage{longMessage(90000)}}
	if !promptTruncated(dropped, 8194) {
		t.Fatal("обрезка не обнаружена")
	}

	// 37 000 символов дают около 11 000 токенов — это нормальная работа.
	kept := ChatRequest{Messages: []ChatMessage{longMessage(37000)}}
	if promptTruncated(kept, 11039) {
		t.Fatal("ложное срабатывание на нормальном промпте")
	}

	// Рантайм не сообщил счётчик — судить не о чем, не обвиняем зря.
	if promptTruncated(kept, 0) {
		t.Fatal("ложное срабатывание без счётчика")
	}
}

// Окно контекста берётся из конфигурации, а пустое значение означает умолчание.
func TestNumCtxOfUsesConfigOverride(t *testing.T) {
	if got := NumCtxOf(workercfgForTest(0)); got != DefaultNumCtx {
		t.Fatalf("ожидалось окно по умолчанию %d, получено %d", DefaultNumCtx, got)
	}
	if got := NumCtxOf(workercfgForTest(32768)); got != 32768 {
		t.Fatalf("окно из конфигурации не применено: %d", got)
	}
}
