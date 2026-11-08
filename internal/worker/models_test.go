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

func TestModelNamesMatchTag(t *testing.T) {
	if !modelNamesMatch("gpt-oss:20b:latest", "gpt-oss:20b") {
		t.Fatal("тег latest должен совпасть с именем без тега")
	}
	if modelNamesMatch("qwen3:8b", "gpt-oss:20b") {
		t.Fatal("разные модели совпали")
	}
}

func TestMeshDoesNotHideAMemoryRefusal(t *testing.T) {
	if meshCanFallBack("shard group unavailable: insufficient GPU memory") {
		t.Fatal("нехватка памяти не должна уводить ответ в локальную модель")
	}
	if meshCanFallBack("mesh runtime did not become ready in 45m0s; no output") {
		t.Fatal("молчащая группа не должна уводить ответ в локальную модель")
	}
	if !meshCanFallBack("shard runtime unavailable: connection refused") {
		t.Fatal("сбой запуска рантайма всё ещё можно закрыть локальной моделью")
	}
	if meshCanFallBack("prompt does not fit the shard-group context window: at least 9 tokens against 4") {
		t.Fatal("длинный промпт нельзя тихо перекладывать")
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
