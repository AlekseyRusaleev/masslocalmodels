package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/private-compute/network/internal/store"
)

// Параметры базы знаний.
const (
	// chunkChars — размер чанка в символах. Меньше — точнее поиск, но больше
	// векторов и запросов; больше — дешевле, но размывает смысл.
	chunkChars = 800
	// chunkOverlap — перекрытие соседних чанков: факт на границе не должен
	// теряться ни в одном из них.
	chunkOverlap = 120
	// maxDocumentChars — предел документа.
	maxDocumentChars = 400_000
	// embedBatch — сколько чанков считать одним заданием.
	embedBatch = 32
)

// registerRAGRoutes подключает базу знаний: загрузку, поиск и удаление.
//
// Векторы считаются тем же путём, что и /v1/embeddings: задание в очередь,
// узел считает, сервер сохраняет. Отдельного «индексатора» в сервисе нет —
// иначе модель эмбеддингов пришлось бы держать в двух местах.
func (cfg Config) registerRAGRoutes(mux *http.ServeMux, hubRef *hub) {
	mux.Handle("GET /v1/rag/documents", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		documents, err := cfg.Store.ListDocuments(r.Context(), p.UserID, 50)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list documents")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": documents})
	}))

	mux.Handle("POST /v1/rag/documents", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		if !cfg.rateLimited(w, p) {
			return
		}
		var body struct {
			Title  string `json:"title"`
			Source string `json:"source"`
			Text   string `json:"text"`
			Model  string `json:"model"`
		}
		if err := decodeJSONLimit(w, r, &body, maxEmbedBodyBytes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		text := strings.TrimSpace(body.Text)
		if text == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "text is required")
			return
		}
		if len(text) > maxDocumentChars {
			writeError(w, http.StatusBadRequest, "invalid_request", "document is too large")
			return
		}

		title := strings.TrimSpace(body.Title)
		if title == "" {
			title = firstLine(text, 80)
		}

		pieces := splitIntoChunks(text)
		model, err := cfg.embeddingModel(r, body.Model)
		if err != nil {
			writeError(w, http.StatusNotFound, "no_embedding_model",
				"no worker serves an embedding model"+availableSuffix(r, cfg, ""))
			return
		}

		vectors, err := cfg.embedTexts(r, hubRef, p, model, pieces)
		if err != nil {
			writeError(w, http.StatusBadGateway, "embedding_failed", err.Error())
			return
		}

		chunks := make([]store.Chunk, 0, len(pieces))
		for index, piece := range pieces {
			chunks = append(chunks, store.Chunk{Ordinal: index, Content: piece, Embedding: vectors[index]})
		}

		document, err := cfg.Store.CreateDocument(r.Context(), p.UserID, title, strings.TrimSpace(body.Source), chunks)
		if err != nil {
			cfg.Log.Error("cannot save document", "err", err, "chunks", len(chunks))
			writeError(w, http.StatusInternalServerError, "internal", "cannot save document")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"document": document, "model": model})
	}))

	mux.Handle("DELETE /v1/rag/documents/{id}", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		if err := cfg.Store.DeleteDocument(r.Context(), p.UserID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "document not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot delete document")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	}))

	mux.Handle("POST /v1/rag/search", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		if !cfg.rateLimited(w, p) {
			return
		}
		var body struct {
			Query string `json:"query"`
			Model string `json:"model"`
			Limit int    `json:"limit"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		query := strings.TrimSpace(body.Query)
		if query == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "query is required")
			return
		}

		model, err := cfg.embeddingModel(r, body.Model)
		if err != nil {
			writeError(w, http.StatusNotFound, "no_embedding_model",
				"no worker serves an embedding model"+availableSuffix(r, cfg, ""))
			return
		}
		vectors, err := cfg.embedTexts(r, hubRef, p, model, []string{query})
		if err != nil {
			writeError(w, http.StatusBadGateway, "embedding_failed", err.Error())
			return
		}

		hits, err := cfg.Store.SearchChunks(r.Context(), p.UserID, vectors[0], body.Limit)
		if err != nil {
			cfg.Log.Error("cannot search knowledge base", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "cannot search")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"model":  model,
			"data":   hits,
		})
	}))
}

// embeddingModel выбирает модель: явная из запроса или первая обслуживаемая.
func (cfg Config) embeddingModel(r *http.Request, requested string) (string, error) {
	if model := strings.TrimSpace(requested); model != "" {
		nodes, err := cfg.Store.ServingEmbeddingNodes(r.Context(), model, servingWindow)
		if err != nil {
			return "", err
		}
		if len(nodes) == 0 {
			return "", errNoEmbeddingModel
		}
		return model, nil
	}
	return cfg.Store.EmbeddingModel(r.Context(), servingWindow)
}

// errNoEmbeddingModel — модель эмбеддингов не объявлена ни одним узлом.
var errNoEmbeddingModel = errors.New("no embedding model")

// embedTexts считает векторы через очередь заданий: пачками, чтобы не гонять
// по одному запросу на каждый чанк.
func (cfg Config) embedTexts(r *http.Request, hubRef *hub, p principal, model string, inputs []string) ([][]float64, error) {
	vectors := make([][]float64, 0, len(inputs))

	for start := 0; start < len(inputs); start += embedBatch {
		end := start + embedBatch
		if end > len(inputs) {
			end = len(inputs)
		}
		batch := inputs[start:end]

		vectors32, _, err := cfg.runEmbeddingJob(r, hubRef, p, model, batch)
		if err != nil {
			return nil, err
		}
		for _, vector := range vectors32 {
			converted := make([]float64, len(vector))
			for index, value := range vector {
				converted[index] = float64(value)
			}
			vectors = append(vectors, converted)
		}
	}
	return vectors, nil
}

// runEmbeddingJob ставит задание эмбеддингов и ждёт векторы вместе со счётчиком
// токенов: он нужен клиенту в usage.
func (cfg Config) runEmbeddingJob(r *http.Request, hubRef *hub, p principal, model string, inputs []string) ([][]float32, int, error) {
	request := EmbedRequest{Model: model, Input: inputs}
	job, err := cfg.Store.CreateJob(r.Context(), model, "embedding", request, 0, p.UserID, "private")
	if err != nil {
		return nil, 0, err
	}

	streamRef := hubRef.open(job.ID)
	defer hubRef.close(job.ID)

	timeout := time.NewTimer(chatTimeout)
	defer timeout.Stop()

	select {
	case <-streamRef.done:
	case <-timeout.C:
		_ = cfg.Store.SetJobState(r.Context(), job.ID, "failed", "embedding timed out")
		return nil, 0, errors.New("worker did not answer in time")
	case <-r.Context().Done():
		return nil, 0, errors.New("request cancelled")
	}

	result := streamRef.outcome()
	if strings.TrimSpace(result.Error) != "" {
		return nil, 0, errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return nil, 0, errors.New("worker returned an empty result")
	}

	var payload EmbedResult
	if err := json.Unmarshal(result.Result, &payload); err != nil {
		return nil, 0, errors.New("worker returned unreadable result")
	}
	if len(payload.Embeddings) != len(inputs) {
		return nil, 0, errors.New("worker returned wrong number of vectors")
	}
	return payload.Embeddings, result.PromptTokens, nil
}

// splitIntoChunks режет текст на части по абзацам с перекрытием.
//
// Режем по границе абзаца, а не по символу: разрезанный посреди фразы чанк
// теряет смысл, и поиск по нему работает хуже.
func splitIntoChunks(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	paragraphs := strings.Split(text, "\n\n")

	chunks := make([]string, 0, 8)
	var current strings.Builder

	flush := func() {
		piece := strings.TrimSpace(current.String())
		if piece == "" {
			return
		}
		chunks = append(chunks, piece)
		// Перекрытие: хвост предыдущего чанка попадает в начало следующего.
		// Хвост берём по рунам: байтовый срез разрезал бы букву пополам, и
		// Postgres отверг бы такой текст как невалидный UTF-8.
		current.Reset()
		current.WriteString(tailRunes(piece, chunkOverlap))
	}

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		// Слишком длинный абзац режем по предложениям.
		for _, piece := range splitLongParagraph(paragraph) {
			if current.Len()+len(piece)+2 > chunkChars && current.Len() > 0 {
				flush()
			}
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(piece)
		}
	}
	flush()
	return chunks
}

// tailRunes возвращает последние n рун строки (или строку целиком).
func tailRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}

// splitLongParagraph делит абзац, который больше чанка, по предложениям.
func splitLongParagraph(paragraph string) []string {
	if len(paragraph) <= chunkChars {
		return []string{paragraph}
	}

	pieces := make([]string, 0, 4)
	var current strings.Builder
	for _, sentence := range splitSentences(paragraph) {
		if current.Len()+len(sentence) > chunkChars && current.Len() > 0 {
			pieces = append(pieces, strings.TrimSpace(current.String()))
			current.Reset()
		}
		current.WriteString(sentence)
	}
	if strings.TrimSpace(current.String()) != "" {
		pieces = append(pieces, strings.TrimSpace(current.String()))
	}
	return pieces
}

// splitSentences режет текст по концам предложений, сохраняя разделители.
func splitSentences(text string) []string {
	const ends = ".!?\n"
	out := make([]string, 0, 16)
	start := 0
	for index, symbol := range text {
		if !strings.ContainsRune(ends, symbol) {
			continue
		}
		out = append(out, text[start:index+utf8.RuneLen(symbol)])
		start = index + utf8.RuneLen(symbol)
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// firstLine — заголовок по умолчанию: первая строка документа.
func firstLine(text string, limit int) string {
	if index := strings.IndexByte(text, '\n'); index > 0 {
		text = text[:index]
	}
	text = strings.TrimSpace(text)
	// Режем по рунам: байтовое обрезание ломает UTF-8 и оставляет «полурку».
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	if text == "" {
		return "Документ"
	}
	return text
}
