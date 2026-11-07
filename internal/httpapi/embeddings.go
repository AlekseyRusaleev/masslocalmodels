package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// EmbedRequest — тело задания «embedding»: что считать.
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbedResult — ответ рантайма, который узел присылает целиком.
type EmbedResult struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// registerEmbeddingRoutes подключает OpenAI-совместимый /v1/embeddings.
//
// Задание идёт через ту же очередь, что и чат: узел забирает его, считает
// векторы своим рантаймом и возвращает результат целиком. Поток здесь не нужен:
// эмбеддинг — это готовый набор чисел, а не текст по кусочкам.
func (cfg Config) registerEmbeddingRoutes(mux *http.ServeMux, hubRef *hub) {
	mux.Handle("POST /v1/embeddings", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		if !cfg.rateLimited(w, p) {
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, maxEmbedBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot read body")
			return
		}
		request, err := parseEmbedRequest(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		nodes, err := cfg.Store.ServingEmbeddingNodesForOwner(r.Context(), request.Model, p.UserID, servingWindow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot check model availability")
			return
		}
		if len(nodes) == 0 {
			writeError(w, http.StatusConflict, "no_owned_worker",
				"embeddings run only on your machines, and none of them is serving "+request.Model)
			return
		}

		vectors, promptTokens, err := cfg.runEmbeddingJob(r, hubRef, p, request.Model, request.Input)
		if err != nil {
			writeError(w, http.StatusBadGateway, "inference_failed", err.Error())
			return
		}

		data := make([]map[string]any, 0, len(vectors))
		for index, vector := range vectors {
			data = append(data, map[string]any{
				"object":    "embedding",
				"index":     index,
				"embedding": vector,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   data,
			"model":  request.Model,
			"usage": map[string]any{
				"prompt_tokens": promptTokens,
				"total_tokens":  promptTokens,
			},
		})
	}))
}

// maxEmbedBodyBytes — предел тела запроса эмбеддингов.
const maxEmbedBodyBytes = 4 << 20

// parseEmbedRequest принимает input как строку или как массив строк — так же,
// как OpenAI: клиенты присылают оба варианта.
func parseEmbedRequest(raw []byte) (EmbedRequest, error) {
	var body struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return EmbedRequest{}, errors.New("invalid JSON body")
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		return EmbedRequest{}, errors.New("model is required")
	}

	var inputs []string
	var single string
	if err := json.Unmarshal(body.Input, &single); err == nil {
		inputs = []string{single}
	} else if err := json.Unmarshal(body.Input, &inputs); err != nil {
		return EmbedRequest{}, errors.New("input must be a string or an array of strings")
	}

	cleaned := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" {
			continue
		}
		cleaned = append(cleaned, input)
	}
	if len(cleaned) == 0 {
		return EmbedRequest{}, errors.New("input is empty")
	}
	if len(cleaned) > 64 {
		return EmbedRequest{}, errors.New("too many inputs: up to 64 per request")
	}
	return EmbedRequest{Model: model, Input: cleaned}, nil
}
