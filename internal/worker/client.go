package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/private-compute/network/internal/store"
)

// Client — HTTPS-клиент к Control Plane. Все соединения исходящие.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// stream — клиент без общего таймаута: поток чанков живёт столько, сколько
	// идёт генерация. Ограничение задаётся контекстом задания.
	stream *http.Client
}

// NewClient создаёт клиент для указанного Control Plane.
func NewClient(baseURL, token string) *Client {
	transport := &http.Transport{
		MaxIdleConns:        4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		stream: &http.Client{Transport: transport},
	}
}

// streamHTTP возвращает клиент для длинных потоковых запросов.
func (c *Client) streamHTTP() *http.Client { return c.stream }

// Retarget обновляет адрес Control Plane и секрет устройства, сохраняя пул
// соединений: иначе каждый heartbeat начинается с нового TLS-рукопожатия, и
// измеренный RTT перестаёт отражать задержку сети.
func (c *Client) Retarget(baseURL, token string) {
	c.baseURL = strings.TrimRight(baseURL, "/")
	c.token = token
}

// Enroll регистрирует узел по одноразовому токену подключения.
func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (EnrollResponse, error) {
	var resp EnrollResponse
	err := c.post(ctx, "/worker/v1/enroll", req, &resp, false)
	return resp, err
}

// Heartbeat отправляет телеметрию и получает желаемое состояние.
func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) (HeartbeatResponse, error) {
	var resp HeartbeatResponse
	err := c.post(ctx, "/worker/v1/heartbeat", req, &resp, true)
	return resp, err
}

// AssignmentsResponse — ответ платформы на запрос «что мне держать».
type AssignmentsResponse struct {
	WorkerID    string                 `json:"worker_id"`
	Assignments []store.TopologyDetail `json:"assignments"`
}

// Assignments сообщает, в каких топологиях участвует узел и какие слои держит.
//
// Это источник правды для шард-режима: роль, диапазон слоёв и соисполнителей
// задаёт топология на платформе, а не файл на каждой машине.
func (c *Client) Assignments(ctx context.Context) (AssignmentsResponse, error) {
	var out AssignmentsResponse

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/worker/v1/assignments", nil)
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("assignments request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("assignments: http %d: %s", resp.StatusCode, serverError(raw))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode assignments: %w", err)
	}
	return out, nil
}

// Manifest запрашивает у Control Plane список артефактов ревизии.
func (c *Client) Manifest(ctx context.Context, revisionID string) (store.Manifest, error) {
	var manifest store.Manifest

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/worker/v1/artifacts/manifest?revision="+url.QueryEscape(revisionID), nil)
	if err != nil {
		return manifest, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return manifest, fmt.Errorf("manifest request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return manifest, fmt.Errorf("manifest: http %d: %s", resp.StatusCode, serverError(raw))
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

// post выполняет POST с JSON-телом и разбирает ответ.
func (c *Client) post(ctx context.Context, path string, body, out any, authorized bool) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if authorized && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 64<<10)
	raw, _ := io.ReadAll(limited)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: http %d: %s", path, resp.StatusCode, serverError(raw))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// serverError вытаскивает текст ошибки из ответа Control Plane.
func serverError(raw []byte) string {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		if envelope.Error.Code != "" {
			return envelope.Error.Code + ": " + envelope.Error.Message
		}
		return envelope.Error.Message
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	return trimmed
}
