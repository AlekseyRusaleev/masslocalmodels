package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ServingModel — модель, которую сеть обслуживает прямо сейчас.
type ServingModel struct {
	ID       string   `json:"id"`
	ServedBy []string `json:"served_by"`
}

// FetchServingModels спрашивает у платформы каталог обслуживаемых моделей.
//
// Нужен и человеку (посмотреть, что доступно), и установщику: модель выбирает
// сеть, а участник не должен ничего решать.
func FetchServingModels(ctx context.Context, controlURL, apiKey string) ([]ServingModel, error) {
	if controlURL == "" {
		return nil, fmt.Errorf("не указан адрес платформы")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("нет ключа участника для запроса каталога")
	}

	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	url := strings.TrimRight(controlURL, "/") + "/v1/models"
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("платформа недоступна: %w", err)
	}
	defer response.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("каталог моделей: http %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}

	var body struct {
		Data []ServingModel `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("не разобран ответ каталога: %w", err)
	}
	return body.Data, nil
}

// FetchServedModel возвращает модель, которую обслуживает больше всего узлов.
//
// Если моделей несколько, выбирается та, у которой больше узлов: это и есть
// «модель, установленная системой» в терминах участника.
func FetchServedModel(ctx context.Context, controlURL, apiKey string) (string, error) {
	models, err := FetchServingModels(ctx, controlURL, apiKey)
	if err != nil {
		return "", err
	}
	best := ""
	bestNodes := -1
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		count := len(model.ServedBy)
		if count > bestNodes {
			best = model.ID
			bestNodes = count
		}
	}
	return best, nil
}
