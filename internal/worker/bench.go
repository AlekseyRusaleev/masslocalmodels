package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaEndpoint — локальный адрес рантайма для замера.
const OllamaEndpoint = "http://127.0.0.1:11434"

// BenchResult — результат калибровочного замера.
type BenchResult struct {
	Model          string  `json:"model"`
	ModelBillions  float64 `json:"model_billions"`
	Quantization   string  `json:"quantization"`
	TokensPerSec   float64 `json:"tokens_per_second"`
	GeneratedToken int     `json:"generated_tokens"`
	WallSeconds    float64 `json:"wall_seconds"`
	LoadMS         float64 `json:"load_ms"`
}

// BenchViaOllama прогоняет короткую генерацию на локальном рантайме и считает
// скорость декода по фактическим полям ответа (eval_count / eval_duration).
func BenchViaOllama(ctx context.Context, model string, maxTokens int) (BenchResult, error) {
	if maxTokens <= 0 {
		maxTokens = 96
	}

	payload := map[string]any{
		"model":  model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": "List five short bullet points about distributed inference."},
		},
		"options": map[string]any{"num_ctx": 2048, "temperature": 0.2, "num_predict": maxTokens},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench: encode: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, OllamaEndpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return BenchResult{}, fmt.Errorf("bench: runtime unavailable: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return BenchResult{}, fmt.Errorf("bench: runtime http %d: %s", resp.StatusCode, serverError(raw))
	}

	var parsed struct {
		EvalCount    int   `json:"eval_count"`
		EvalDuration int64 `json:"eval_duration"`
		LoadDuration int64 `json:"load_duration"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return BenchResult{}, fmt.Errorf("bench: decode: %w", err)
	}
	if parsed.EvalCount == 0 || parsed.EvalDuration == 0 {
		return BenchResult{}, fmt.Errorf("bench: runtime returned no timing data")
	}

	return BenchResult{
		Model:          model,
		TokensPerSec:   float64(parsed.EvalCount) / (float64(parsed.EvalDuration) / 1e9),
		GeneratedToken: parsed.EvalCount,
		WallSeconds:    time.Since(started).Seconds(),
		LoadMS:         float64(parsed.LoadDuration) / 1e6,
	}, nil
}

// ReportBenchmark отправляет результат калибровки на Control Plane.
func (c *Client) ReportBenchmark(ctx context.Context, result BenchResult) error {
	payload := map[string]any{
		"model":             result.Model,
		"model_billions":    result.ModelBillions,
		"quantization":      result.Quantization,
		"tokens_per_second": result.TokensPerSec,
		"generated_tokens":  result.GeneratedToken,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/worker/v1/benchmark", bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return fmt.Errorf("bench: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bench: report: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("bench: report http %d: %s", resp.StatusCode, serverError(raw))
	}
	return nil
}

// mustJSON кодирует значение, паникуя только на несериализуемых данных.
func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return data
}
