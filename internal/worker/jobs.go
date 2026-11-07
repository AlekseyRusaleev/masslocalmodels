package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/private-compute/network/internal/workercfg"
)

// Job — задание, полученное от Control Plane.
type Job struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ChatRequest — тело задания «chat»: Control Plane уже привёл его к виду
// рантайма (content строкой, аргументы инструментов объектом).
type ChatRequest struct {
	Model       string          `json:"model"`
	Messages    []ChatMessage   `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Seed        *int            `json:"seed"`
	Stop        json.RawMessage `json:"stop"`
	Tools       json.RawMessage `json:"tools"`
}

// EmbedRequest — тело задания «embedding»: что считать.
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// ChatMessage — сообщение диалога в форме рантайма.
type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall — вызов инструмента в истории диалога.
type ToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

const (
	// DefaultNumCtx — окно контекста по умолчанию для всех заданий.
	//
	// Почему одно значение на все запросы, а не «под размер запроса»: смена
	// num_ctx заставляет Ollama перезагружать модель (2–6 с) и терять кэш
	// промпта, поэтому окно фиксируется на весь сеанс узла.
	//
	// 16384 измерено на RTX 5060 8 GB с OLLAMA_KV_CACHE_TYPE=q8_0 и
	// OLLAMA_FLASH_ATTENTION=1: кэш занимает вдвое меньше VRAM, модель целиком
	// на GPU (6744 MiB), скорость 70.6 tok/s — столько же, сколько на 4K.
	// Без квантования кэша 16K уезжало в CPU и давало 29.9 tok/s.
	DefaultNumCtx = 16384
	// keepAlive держит модель в памяти 30 минут: иначе Ollama выгружает её
	// через пять минут и следующий запрос ждёт загрузки.
	keepAlive = "30m"
)

// EmbeddingModelOf выбирает модель эмбеддингов для узла.
//
// Явное значение из конфигурации важнее догадки. Если его нет, берём первую
// локальную модель, в имени которой есть «embed»: у Ollama нет «списка
// embedding-моделей», а угадывать по архитектуре мы не можем.
func EmbeddingModelOf(cfg workercfg.Config) string {
	if name := strings.TrimSpace(cfg.EmbeddingModel); name != "" {
		return name
	}
	models, err := OllamaModels(context.Background())
	if err != nil {
		return ""
	}
	for _, name := range models {
		if strings.Contains(strings.ToLower(name), "embed") {
			return name
		}
	}
	return ""
}

// NumCtxOf возвращает окно контекста узла: значение из конфигурации, а если оно
// не задано — значение по умолчанию.
func NumCtxOf(cfg workercfg.Config) int {
	if cfg.NumCtx > 0 {
		return cfg.NumCtx
	}
	return DefaultNumCtx
}

// charsPerTokenMax — верхняя граница «сколько символов приходится на один токен»
// для любой модели семейства Qwen. Русский текст даёт 2–3 символа на токен,
// английский — около 4, редкие длинные слова — до 6. Значение нужно как нижняя
// оценка числа токенов: если рантайм насчитал меньше, чем text/6, значит часть
// промпта он не прочитал.
const charsPerTokenMax = 6

// MinPromptTokens оценивает, сколько токенов во входе как минимум.
//
// Оценка снизу: она не может сказать «промпт больше окна» там, где он на самом
// деле влезает, поэтому ею можно отказывать в задании, не боясь отвергнуть
// корректный запрос.
func MinPromptTokens(request ChatRequest) int {
	chars := 0
	for _, message := range request.Messages {
		chars += len([]rune(message.Content))
	}
	chars += len(request.Tools)
	return chars / charsPerTokenMax
}

// promptTruncated проверяет, потерял ли рантайм часть промпта.
//
// Ollama молча обрезает вход, если он не влезает в окно контекста: счётчик
// prompt_eval_count оказывается меньше, чем символов в запросе. Ловить это
// нужно обязательно — иначе клиент получает правдоподобный ответ по обрывку
// разговора и не знает об этом.
func promptTruncated(request ChatRequest, promptTokens int) bool {
	if promptTokens <= 0 {
		// Рантайм не сообщил счётчик: судить не о чем.
		return false
	}
	return MinPromptTokens(request) > promptTokens
}

// PullJob забирает задание long-poll'ом. nil означает «заданий нет».
func (c *Client) PullJob(ctx context.Context) (*Job, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/worker/v1/jobs/pull", nil)
	if err != nil {
		return nil, fmt.Errorf("jobs: build pull: %w", err)
	}
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	// Long-poll на сервере 25 с, даём запас.
	pullClient := &http.Client{Timeout: 40 * time.Second}
	resp, err := pullClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jobs: pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jobs: pull http %d: %s", resp.StatusCode, serverError(raw))
	}

	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("jobs: decode: %w", err)
	}
	return &job, nil
}

// PostChunk отправляет один кусок ответа отдельным запросом.
// Рабочий путь — ChunkStream ниже; этот метод остаётся для отладки.
func (c *Client) PostChunk(ctx context.Context, jobID, content, thinking string) error {
	payload, _ := json.Marshal(map[string]string{"content": content, "thinking": thinking})
	return c.postAuthorized(ctx, "/worker/v1/jobs/"+jobID+"/chunk", payload)
}

// ChunkStream — открытый поток чанков к Control Plane.
//
// Один HTTP-запрос на весь ответ: чанки пишутся в тело запроса в формате NDJSON
// и уходят по мере генерации. Без этого на каждый токен приходился отдельный
// round-trip, и на канале с RTT 50–250 мс ответ растягивался в разы.
type ChunkStream struct {
	pipe   *io.PipeWriter
	encode *json.Encoder
	result chan error
}

// ChunkStream открывает поток чанков для задания.
func (c *Client) ChunkStream(ctx context.Context, jobID string) (*ChunkStream, error) {
	reader, writer := io.Pipe()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/worker/v1/jobs/"+jobID+"/chunkstream", reader)
	if err != nil {
		_ = writer.CloseWithError(err)
		return nil, fmt.Errorf("jobs: build chunk stream: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	stream := &ChunkStream{
		pipe:   writer,
		encode: json.NewEncoder(writer),
		result: make(chan error, 1),
	}

	// Ответ читаем отдельной горутиной: генерация не должна его ждать.
	go func() {
		resp, err := c.streamHTTP().Do(req)
		if err != nil {
			stream.result <- fmt.Errorf("chunk stream: %w", err)
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		if resp.StatusCode >= 300 {
			stream.result <- fmt.Errorf("chunk stream http %d: %s", resp.StatusCode, serverError(raw))
			return
		}
		stream.result <- nil
	}()

	return stream, nil
}

// chunkPiece — кусок ответа, который узел отправляет Control Plane.
// Аргументы инструментов передаются строкой JSON, как их ждёт клиент OpenAI.
type chunkPiece struct {
	Content   string      `json:"content,omitempty"`
	Thinking  string      `json:"thinking,omitempty"`
	ToolCalls []chunkCall `json:"tool_calls,omitempty"`
}

// chunkCall — вызов инструмента в куске ответа.
type chunkCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Send пишет один кусок ответа в поток.
func (s *ChunkStream) Send(p chunkPiece) error {
	return s.encode.Encode(p)
}

// Close закрывает поток и ждёт подтверждения сервера.
func (s *ChunkStream) Close() error {
	_ = s.pipe.Close()
	return <-s.result
}

// Abort обрывает поток с ошибкой, не дожидаясь сервера.
func (s *ChunkStream) Abort(err error) {
	_ = s.pipe.CloseWithError(err)
}

// JobResult — итог задания: текст ошибки и счётчики рантайма.
type JobResult struct {
	Error            string `json:"error"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	EvalMS           int64  `json:"eval_ms"`
	FinishReason     string `json:"finish_reason"`

	// Чем узел реально обслужил задание: имя модели и digest весов из рантайма.
	// Платформа сверяет имя с запрошенным — это защита от подмены модели.
	ServedModel  string `json:"served_model,omitempty"`
	ServedDigest string `json:"served_digest,omitempty"`

	// Result — данные ответа для нестриминговых заданий (эмбеддинги).
	Result json.RawMessage `json:"result,omitempty"`
}

// FinishJob закрывает задание, при необходимости с текстом ошибки и счётчиками.
func (c *Client) FinishJob(ctx context.Context, jobID string, res JobResult) error {
	payload, _ := json.Marshal(res)
	return c.postAuthorized(ctx, "/worker/v1/jobs/"+jobID+"/done", payload)
}

// postAuthorized выполняет POST с device token и не разбирает тело ответа.
func (c *Client) postAuthorized(ctx context.Context, path string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("jobs: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jobs: request %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("jobs: %s http %d: %s", path, resp.StatusCode, serverError(raw))
	}
	return nil
}

// RunChatJob выполняет задание: стримит локальный рантайм и отправляет чанки.
// Результат содержит счётчики рантайма и текст ошибки (пустой при успехе).
func RunChatJob(ctx context.Context, client *Client, job *Job, defaultModel string, numCtx int, log *slog.Logger) JobResult {
	if numCtx <= 0 {
		numCtx = DefaultNumCtx
	}
	fail := func(msg string) JobResult { return JobResult{Error: msg} }

	var request ChatRequest
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &request); err != nil {
			return fail("cannot parse job payload: " + err.Error())
		}
	}

	model := request.Model
	if model == "" {
		model = job.Model
	}
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return fail("no model specified for the job")
	}

	// Рантайм молча обрезает вход, который не влезает в окно. Если по нижней
	// оценке промпт заведомо больше окна, отказываем сразу: иначе участник
	// потратит минуту на обработку и получит ответ по обрывку диалога.
	if minTokens := MinPromptTokens(request); minTokens > numCtx {
		log.Warn("prompt is larger than the node context window",
			"job", job.ID, "min_tokens", minTokens, "num_ctx", numCtx)
		return fail(fmt.Sprintf(
			"prompt does not fit the node context window: at least %d tokens against %d; "+
				"shorten the conversation or use a node with a larger window",
			minTokens, numCtx))
	}

	payload := map[string]any{
		"model":      model,
		"stream":     true,
		"messages":   request.Messages,
		"keep_alive": keepAlive,
	}
	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}

	// Окно контекста задаётся узлом: Ollama перезагружает модель при его смене,
	// поэтому размер окна задаётся сверху и одинаков для всех заданий.
	options := map[string]any{"num_ctx": numCtx}
	if request.MaxTokens > 0 {
		options["num_predict"] = request.MaxTokens
	}
	if request.Temperature != nil {
		options["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		options["top_p"] = *request.TopP
	}
	if request.Seed != nil {
		options["seed"] = *request.Seed
	}
	if len(request.Stop) > 0 && string(request.Stop) != "null" {
		options["stop"] = request.Stop
	}
	payload["options"] = options

	body, err := json.Marshal(payload)
	if err != nil {
		return fail("cannot encode request: " + err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OllamaEndpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return fail("cannot build runtime request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail("runtime unavailable: " + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail(fmt.Sprintf("runtime http %d: %s", resp.StatusCode, serverError(raw)))
	}

	started := time.Now()
	chunks, contentBytes, toolCalls := 0, 0, 0

	// Один поток на весь ответ: чанки уходят по мере генерации.
	stream, err := client.ChunkStream(ctx, job.ID)
	if err != nil {
		return fail("cannot open chunk stream: " + err.Error())
	}

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		if ctx.Err() != nil {
			stream.Abort(ctx.Err())
			return fail("cancelled")
		}
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var chunk struct {
				Message struct {
					Content   string `json:"content"`
					Thinking  string `json:"thinking"`
					ToolCalls []struct {
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				Done            bool   `json:"done"`
				DoneReason      string `json:"done_reason"`
				PromptEvalCount int    `json:"prompt_eval_count"`
				EvalCount       int    `json:"eval_count"`
				EvalDuration    int64  `json:"eval_duration"`
			}
			if err := json.Unmarshal(line, &chunk); err == nil {
				piece := chunkPiece{
					Content:  chunk.Message.Content,
					Thinking: chunk.Message.Thinking,
				}
				for _, call := range chunk.Message.ToolCalls {
					arguments := string(call.Function.Arguments)
					if arguments == "" {
						arguments = "{}"
					}
					piece.ToolCalls = append(piece.ToolCalls, chunkCall{
						Name:      call.Function.Name,
						Arguments: arguments,
					})
				}
				if piece.Content != "" || piece.Thinking != "" || len(piece.ToolCalls) > 0 {
					if err := stream.Send(piece); err != nil {
						stream.Abort(err)
						log.Warn("chunk delivery failed", "err", err)
						return fail("chunk delivery failed: " + err.Error())
					}
					chunks++
					contentBytes += len(piece.Content)
					toolCalls += len(piece.ToolCalls)
				}
				if chunk.Done {
					result := JobResult{
						PromptTokens:     chunk.PromptEvalCount,
						CompletionTokens: chunk.EvalCount,
						EvalMS:           chunk.EvalDuration / 1_000_000,
						FinishReason:     chunk.DoneReason,
					}
					// Доказательство модели: спрашиваем рантайм, что он реально
					// держит в памяти. Платформа сверит имя с запрошенным.
					result.ServedModel, result.ServedDigest = LoadedModel(ctx, model)
					if result.FinishReason == "" {
						result.FinishReason = "stop"
					}
					if toolCalls > 0 {
						result.FinishReason = "tool_calls"
					}
					// Ollama обрезает промпт молча, оставляя хвост разговора.
					// Признак обрезки: рантайм насчитал меньше токенов, чем
					// символов в запросе (в токене не больше шести символов).
					// Отвечать как ни в чём не бывало нельзя: клиент получит
					// правдоподобный, но неверный ответ по обрывку.
					if promptTruncated(request, result.PromptTokens) || result.PromptTokens >= numCtx-16 {
						log.Warn("context window overflow: prompt was truncated",
							"job", job.ID, "prompt_tokens", result.PromptTokens,
							"min_tokens", MinPromptTokens(request),
							"completion_tokens", result.CompletionTokens, "num_ctx", numCtx)
						result.Error = fmt.Sprintf(
							"prompt does not fit the node context window (%d tokens): the beginning of the "+
								"conversation was dropped; shorten the history or use a node with a larger window",
							numCtx)
					}
					if contentBytes == 0 && toolCalls == 0 && result.FinishReason == "length" {
						// Бюджет токенов кончился на «размышлениях»: ответа нет,
						// но это не ошибка узла — говорим честно, что именно случилось.
						result.Error = "budget exhausted while reasoning; increase max_tokens"
					}
					if err := stream.Close(); err != nil {
						log.Warn("chunk stream closed with error", "job", job.ID, "err", err)
						if result.Error == "" {
							result.Error = "cannot deliver answer: " + err.Error()
						}
						return result
					}
					log.Info("job finished",
						"job", job.ID, "model", model, "chunks", chunks,
						"content_bytes", contentBytes, "tool_calls", toolCalls,
						"tokens_out", result.CompletionTokens,
						"finish", result.FinishReason,
						"generation_ms", result.EvalMS,
						"wall_ms", time.Since(started).Milliseconds())
					return result
				}
			}
		}
		if readErr == io.EOF {
			// Рантайм закрыл поток, не прислав done: закрываем и канал.
			_ = stream.Close()
			return fail("")
		}
		if readErr != nil {
			stream.Abort(readErr)
			if strings.Contains(readErr.Error(), "context canceled") {
				return fail("cancelled")
			}
			return fail("runtime stream error: " + readErr.Error())
		}
	}
}

// RunEmbedJob выполняет задание эмбеддингов: один вызов рантайма, результат целиком.
//
// Поток здесь не нужен: это набор чисел, а не текст по кусочкам. Результат
// уходит в поле result, сервер хранит его рядом с заданием и отдаёт клиенту.
func RunEmbedJob(ctx context.Context, client *Client, job *Job, defaultModel string, log *slog.Logger) JobResult {
	fail := func(msg string) JobResult { return JobResult{Error: msg} }

	var request EmbedRequest
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &request); err != nil {
			return fail("cannot parse embed payload: " + err.Error())
		}
	}
	model := request.Model
	if model == "" {
		model = job.Model
	}
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return fail("no model specified for the job")
	}
	if len(request.Input) == 0 {
		return fail("no input for the embedding")
	}

	payload, err := json.Marshal(map[string]any{"model": model, "input": request.Input})
	if err != nil {
		return fail("cannot encode request: " + err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OllamaEndpoint+"/api/embed", bytes.NewReader(payload))
	if err != nil {
		return fail("cannot build runtime request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")

	started := time.Now()
	// Рантайм может перезапустить модель на лету (например, чтобы загрузить
	// модель эмбеддингов вместо чат-модели) и закрыть соединение. Это разовый
	// сбой, а не ошибка задания: повторяем один раз, прежде чем сдаться.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		time.Sleep(2 * time.Second)
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, OllamaEndpoint+"/api/embed", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fail("runtime unavailable: " + err.Error())
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail(fmt.Sprintf("runtime http %d: %s", resp.StatusCode, serverError(raw)))
	}

	var body struct {
		Embeddings      [][]float32 `json:"embeddings"`
		PromptEvalCount int         `json:"prompt_eval_count"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&body); err != nil {
		return fail("cannot read runtime answer: " + err.Error())
	}
	if len(body.Embeddings) == 0 {
		return fail("runtime returned no embeddings")
	}

	resultData, err := json.Marshal(map[string]any{"embeddings": body.Embeddings})
	if err != nil {
		return fail("cannot encode result: " + err.Error())
	}

	result := JobResult{
		PromptTokens:     body.PromptEvalCount,
		CompletionTokens: 0,
		EvalMS:           time.Since(started).Milliseconds(),
		FinishReason:     "stop",
		Result:           resultData,
	}
	result.ServedModel, result.ServedDigest = LoadedModel(ctx, model)

	log.Info("embedding job finished",
		"job", job.ID, "model", model, "vectors", len(body.Embeddings),
		"dimensions", len(body.Embeddings[0]), "wall_ms", time.Since(started).Milliseconds())
	return result
}

// ServeJobs — главный цикл узла: забрать задание, выполнить, закрыть.
//
// Модель берётся из аргумента, а если он пуст — из конфигурации. Пауза читается
// из конфигурации на каждом проходе, поэтому «worker-agent pause» действует на
// уже запущенный процесс, без перезапуска.
func ServeJobs(ctx context.Context, cfgPath string, model string, log *slog.Logger) error {
	backoff := time.Second
	client := NewClient("", "")

	for {
		if ctx.Err() != nil {
			return nil
		}

		cfg, err := workercfg.Load(cfgPath)
		if err != nil {
			log.Warn("cannot read config, retrying", "err", err)
			if !sleep(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		if cfg.Paused {
			// Пауза: не забираем задания, но и процесс не завершаем.
			if !sleep(ctx, 3*time.Second) {
				return nil
			}
			continue
		}

		if model == "" {
			model = cfg.Model
		}

		client.Retarget(cfg.ControlURL, cfg.DeviceToken)
		cfg = prepareAutomaticMesh(cfg)

		// Узел-стадия держит RPC-сервер поднятым: координатор подключается к
		// нему при загрузке слоёв, а не когда захочет стадия.
		if mesh := MeshOptionsOf(cfg); mesh != nil {
			if stage, stageErr := MeshForStage(cfg); stageErr == nil {
				if _, err := EnsureMeshRuntime(ctx, client, stage, log); err != nil {
					log.Warn("cannot start shard stage", "err", err)
				}
			}
		}

		job, err := client.PullJob(ctx)
		if err != nil {
			log.Warn("pull failed, retrying", "err", err, "retry_in", backoff.String())
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Second
		if job == nil {
			continue
		}

		log.Info("job claimed", "job", job.ID, "model", job.Model)
		ActiveJobs.Add(1)
		// Вид задания определяет обработчик: чат идёт потоком, эмбеддинги —
		// одним результатом. Шард-координатор обслуживает чат через
		// llama-server, потому что Ollama не умеет раскладывать слои по узлам.
		var result JobResult
		mesh := MeshOptionsOf(cfg)
		switch {
		case job.Kind == "embedding":
			result = RunEmbedJob(ctx, client, job, model, log)
		case mesh != nil && mesh.Role == meshRoleAuto:
			// Группы ещё нет — отвечаем моделью целиком. Когда платформа
			// назначит этого узла координатором, чат пойдёт через слои.
			stage, err := MeshForJob(cfg)
			if err != nil {
				if plan, ok := CurrentMeshPlan(); ok && plan.Role == meshRoleCompute {
					result = JobResult{Error: "этот узел держит слои и не отвечает на чат сам"}
					break
				}
				result = RunChatJob(ctx, client, job, model, NumCtxOf(cfg), log)
				break
			}
			// Модель уже в памяти и может ответить. Снимать её до ответа
			// нельзя: прошлый раз из-за этого запрос оборвался, хотя ответ
			// был готов. После ответа карта освобождается сама, и следующее
			// сообщение идёт через группу слоёв.
			if ollamaHasModel(ctx, model) {
				result = RunChatJob(ctx, client, job, model, NumCtxOf(cfg), log)
				if result.Error == "" {
					if stopped := stopOllamaModelServers(); stopped > 0 {
						log.Info("local model stopped after the answer, next request uses the layer group", "processes", stopped)
					}
				}
				break
			}
			result = RunMeshChatJob(ctx, client, job, stage, cfg.NumCtx, log)
			if result.Error != "" && meshCanFallBack(result.Error) {
				log.Warn("shard group did not start, answering with the local model", "err", result.Error)
				result = RunChatJob(ctx, client, job, model, NumCtxOf(cfg), log)
			}
		case mesh != nil:
			stage, err := MeshForJob(cfg)
			if err != nil {
				result = JobResult{Error: "shard group not ready: " + err.Error()}
				break
			}
			result = RunMeshChatJob(ctx, client, job, stage, cfg.NumCtx, log)
		default:
			result = RunChatJob(ctx, client, job, model, cfg.NumCtx, log)
		}
		ActiveJobs.Add(-1)

		if err := client.FinishJob(ctx, job.ID, result); err != nil {
			log.Warn("cannot finish job", "job", job.ID, "err", err)
		}
		if result.Error != "" {
			log.Warn("job failed", "job", job.ID, "err", result.Error)
		}
	}
}

// meshCanFallBack — группа слоёв не стартовала, но локальная модель может ответить.
// Ошибка размера промпта сюда не входит: её повтор через Ollama ничего не исправит.
func meshCanFallBack(err string) bool {
	text := strings.ToLower(err)
	if strings.Contains(text, "context window") {
		return false
	}
	return strings.Contains(text, "insufficient") ||
		strings.Contains(text, "shard group unavailable") ||
		strings.Contains(text, "shard runtime")
}
