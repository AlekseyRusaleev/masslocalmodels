package worker

import (
	"context"
	"testing"
)

func TestResolveChatModel(t *testing.T) {
	if got := ResolveChatModel(""); got != DefaultChatModel {
		t.Fatalf("пустое имя = %q", got)
	}
	if got := ResolveChatModel("qwen3:8b"); got != DefaultChatModel {
		t.Fatalf("снятая модель = %q", got)
	}
	if got := ResolveChatModel("gpt-oss:20b"); got != "gpt-oss:20b" {
		t.Fatalf("своя модель переписана: %q", got)
	}
}

func TestEnsureOllamaDeclinedWhenMissing(t *testing.T) {
	if OllamaReady(context.Background()) || OllamaExecutable() != "" {
		t.Skip("на этой машине Ollama уже есть")
	}
	err := EnsureOllama(context.Background(), EnsureOllamaOptions{Ask: func() bool { return false }})
	if err != ErrOllamaDeclined {
		t.Fatalf("ожидался отказ, получено %v", err)
	}
}
