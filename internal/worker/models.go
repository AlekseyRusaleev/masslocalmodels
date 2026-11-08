package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/private-compute/network/internal/swarm"
)

// DefaultChatModel — модель, которую новый участник скачивает и объявляет.
// Тестовая qwen3:8b снята: это проверка канала, не продукт.
const DefaultChatModel = "qwen2.5:72b-instruct-q4_0"

// Голова черновика EAGLE-3 для GPT-OSS-20B. Обучена только на эту модель:
// к другой не подходит. Файл маленький, живёт рядом с весами.
const (
	DefaultDraftName   = "eagle3-gpt-oss-20b-Q8_0.gguf"
	DefaultDraftURL    = "https://huggingface.co/ggml-org/gpt-oss-20b-GGUF/resolve/main/eagle3-gpt-oss-20b-Q8_0.gguf"
	DefaultDraftSHA256 = "6955f98a701e97a8c969f3ff33be8ba17b35bd60e5ac96258d2c42d710c7d63f"
	DefaultDraftBytes  = 921488000
	DefaultDraftSpec   = "draft-eagle3"
	// SpecNgram ищет повтор в уже имеющемся тексте. Отдельной головы модели
	// ему не нужно.
	SpecNgram = "ngram-mod"
	// SpecNone выключает черновик. Это отдельный режим прогона, а не пропуск флага.
	SpecNone = "none"
)

// ResolveChatModel выбирает имя модели для скачивания и харнеса.
// Пустое имя и снятая тестовая модель заменяются на GPT-OSS-20B.
func ResolveChatModel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, swarm.RetiredModel) {
		return DefaultChatModel
	}
	return name
}

// FetchDesiredModel читает модель, которую сейчас должна держать сеть.
// Файл маленький и лежит рядом с обновлением программы. Пустой ответ значит
// «сервер ничего не назначил».
func FetchDesiredModel(ctx context.Context, controlURL string) (string, error) {
	control := normalizeControl(controlURL)
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, control+"/node/model.json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "compute-network-node/"+AgentVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("модель сети: %s", resp.Status)
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("модель сети не читается")
	}
	return ResolveChatModel(body.Model), nil
}
