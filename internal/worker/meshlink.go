package worker

// Туннель шард-группы со стороны узла (ADR 0008).
//
// RPC-протокол llama.cpp не имеет ни TLS, ни авторизации, поэтому порт стадии
// наружу не выставляется. Обе стороны сами открывают исходящие HTTPS-потоки на
// платформу, а платформа сводит их в пару (internal/httpapi/meshlink.go). Ни
// один порт не открывается наружу: работает и за домашним NAT, и за мобильным
// интернетом.
//
// Схема: два односторонних потока (POST «вверх», GET «вниз») и кадры внутри
// них (internal/worker/meshmux.go). Кадр несёт идентификатор логического
// соединения, поэтому несколько соединений RPC не перемешиваются. Двусторонний
// HTTP-запрос здесь не работает: Go отправляет заголовки запроса только вместе
// с первым куском тела, и молчащая сторона блокирует всю пару.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/private-compute/network/internal/workercfg"
)

const (
	// meshLinkRetry — пауза перед переподключением потока.
	meshLinkRetry = 2 * time.Second
	// meshFrameQueue — сколько кадров держим в очереди на отправку.
	meshFrameQueue = 256
)

// NewClientForConfig создаёт клиент платформы по локальной конфигурации.
func NewClientForConfig(cfgPath string) (*Client, error) {
	cfg, err := workercfg.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if cfg.ControlURL == "" {
		return nil, fmt.Errorf("в конфигурации нет control_url")
	}
	return NewClient(cfg.ControlURL, cfg.DeviceToken), nil
}

// meshFrame — кадр логического соединения.
type meshFrame struct {
	id      uint64
	kind    byte
	payload []byte
}

// meshPipe — пара потоков «вверх» и «вниз» для одной стадии.
type meshPipe struct {
	client     *Client
	topologyID string
	stageID    string
	to         string
	frames     chan meshFrame
	log        func() string
}

// meshLinkURL собирает адрес стороны туннеля.
func meshLinkURL(baseURL, topologyID, stageID, to string) string {
	values := url.Values{}
	values.Set("topology", topologyID)
	values.Set("stage", stageID)
	values.Set("to", to)
	return baseURL + "/worker/v1/mesh/link?" + values.Encode()
}

// runUp держит поток «вверх» и отправляет кадры из очереди.
//
// Переподключение делается здесь же: поток может закрыться по таймауту nginx
// или из-за перезапуска gateway, а туннель должен продолжить работать.
func (p *meshPipe) runUp(ctx context.Context) {
	backlog := make([]meshFrame, 0, 8)
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.upOnce(ctx, &backlog)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.note(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(meshLinkRetry):
		}
	}
}

// upOnce открывает POST и пишет в него кадры до обрыва.
func (p *meshPipe) upOnce(ctx context.Context, backlog *[]meshFrame) error {
	reader, writer := io.Pipe()
	writeErr := make(chan error, 1)

	go func() {
		defer writer.Close()
		writeErr <- func() error {
			// Пульс не даёт прокси закрыть молчащее тело и будит запись,
			// если соединение уже оборвано: иначе повторный поток не откроется.
			pulse := time.NewTicker(20 * time.Second)
			defer pulse.Stop()
			for {
				var frame meshFrame
				if len(*backlog) > 0 {
					frame = (*backlog)[0]
					*backlog = (*backlog)[1:]
				} else {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case frame = <-p.frames:
					case <-pulse.C:
						frame = meshFrame{kind: meshFramePulse}
					}
				}
				if err := writeMeshFrame(writer, frame.id, frame.kind, frame.payload); err != nil {
					if frame.kind == meshFramePulse {
						return err
					}
					// Возвращаем кадр в начало очереди: он ещё не доставлен.
					*backlog = append([]meshFrame{frame}, *backlog...)
					return err
				}
			}
		}()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		meshLinkURL(p.client.baseURL, p.topologyID, p.stageID, p.to), reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Mesh-Role", "up")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if p.client.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.client.token)
	}

	resp, err := p.client.stream.Do(req)
	if err != nil {
		_ = reader.CloseWithError(err)
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("mesh up: http %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	return nil
}

// runDown держит поток «вниз» и раздаёт кадры обработчику.
func (p *meshPipe) runDown(ctx context.Context, handle func(meshFrame) error) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.downOnce(ctx, handle)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.note(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(meshLinkRetry):
		}
	}
}

// downOnce открывает GET и читает кадры до конца потока.
func (p *meshPipe) downOnce(ctx context.Context, handle func(meshFrame) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		meshLinkURL(p.client.baseURL, p.topologyID, p.stageID, p.to), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Mesh-Role", "down")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "compute-network-worker/"+AgentVersion)
	if p.client.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.client.token)
	}

	resp, err := p.client.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("mesh down: http %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}

	return readMeshFrames(bufio.NewReaderSize(resp.Body, 64<<10), func(id uint64, kind byte, payload []byte) error {
		return handle(meshFrame{id: id, kind: kind, payload: payload})
	})
}

// note пишет отказ туннеля в журнал и закрывает локальные соединения.
// Раньше текст ответа выбрасывался, и отказ платформы выглядел как молчание.
func (p *meshPipe) note(err error) {
	if err == nil || p == nil {
		return
	}
	slog.Warn("mesh tunnel stream failed",
		"err", err.Error(), "topology", p.topologyID, "stage", p.stageID, "to", p.to)
	if p.log != nil {
		_ = p.log()
	}
}

// MeshTunnelDialer — локальный порт, ведущий к стадии через платформу.
type MeshTunnelDialer struct {
	listener net.Listener
	cancel   context.CancelFunc
	done     chan struct{}
	frames   chan meshFrame
	nextID   atomic.Uint64

	mu    sync.Mutex
	conns map[uint64]net.Conn
}

// Addr — адрес, который передаётся llama-server в --rpc.
func (d *MeshTunnelDialer) Addr() string {
	if d == nil || d.listener == nil {
		return ""
	}
	return d.listener.Addr().String()
}

// Close прекращает принимать соединения и рвёт текущие туннели.
func (d *MeshTunnelDialer) Close() {
	if d == nil {
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	if d.listener != nil {
		_ = d.listener.Close()
	}
	d.closeAll()
	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
	}
}

// closeAll закрывает все логические соединения: после обрыва потока их
// состояние неизвестно, и продолжать сессию нельзя.
func (d *MeshTunnelDialer) closeAll() {
	d.mu.Lock()
	conns := d.conns
	d.conns = make(map[uint64]net.Conn)
	d.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// StartMeshTunnelDialer поднимает локальный порт, ведущий к стадии.
func StartMeshTunnelDialer(ctx context.Context, client *Client, topologyID, stageID, selfID string) (*MeshTunnelDialer, error) {
	if client == nil {
		return nil, fmt.Errorf("mesh tunnel: нет клиента платформы")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mesh tunnel: cannot listen: %w", err)
	}

	linkCtx, cancel := context.WithCancel(ctx)
	dialer := &MeshTunnelDialer{
		listener: listener,
		cancel:   cancel,
		done:     make(chan struct{}),
		frames:   make(chan meshFrame, meshFrameQueue),
		conns:    make(map[uint64]net.Conn),
	}

	pipe := &meshPipe{
		client:     client,
		topologyID: topologyID,
		stageID:    stageID,
		to:         stageID,
		frames:     dialer.frames,
		log: func() string {
			dialer.closeAll()
			return "mesh up/down поток переподключён"
		},
	}
	downPipe := &meshPipe{
		client:     client,
		topologyID: topologyID,
		stageID:    stageID,
		to:         selfID,
	}

	go pipe.runUp(linkCtx)
	go func() {
		defer close(dialer.done)
		downPipe.runDown(linkCtx, func(frame meshFrame) error {
			dialer.mu.Lock()
			conn := dialer.conns[frame.id]
			dialer.mu.Unlock()
			if frame.kind == meshFrameClose {
				if conn != nil {
					_ = conn.Close()
					dialer.mu.Lock()
					delete(dialer.conns, frame.id)
					dialer.mu.Unlock()
				}
				return nil
			}
			if frame.kind != meshFrameData {
				return nil
			}
			if conn == nil {
				return nil
			}
			_, err := conn.Write(frame.payload)
			return err
		})
	}()

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			dialer.handle(linkCtx, conn)
		}
	}()

	return dialer, nil
}

// handle прокладывает одно локальное соединение через туннель.
func (d *MeshTunnelDialer) handle(ctx context.Context, conn net.Conn) {
	id := d.nextID.Add(1)
	d.mu.Lock()
	d.conns[id] = conn
	d.mu.Unlock()

	buf := make([]byte, meshChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			select {
			case d.frames <- meshFrame{id: id, kind: meshFrameData, payload: payload}:
			case <-ctx.Done():
				_ = conn.Close()
				return
			}
		}
		if err != nil {
			select {
			case d.frames <- meshFrame{id: id, kind: meshFrameClose}:
			case <-ctx.Done():
			}
			break
		}
	}

	d.mu.Lock()
	delete(d.conns, id)
	d.mu.Unlock()
	_ = conn.Close()
}

// meshChunkSize — размер чтения с локального соединения.
const meshChunkSize = 32 << 10

// MeshTunnelListener — сторона стадии: держит потоки и локальные соединения
// к своему RPC-серверу по идентификаторам от координатора.
type MeshTunnelListener struct {
	cancel context.CancelFunc
	done   chan struct{}
	frames chan meshFrame

	mu    sync.Mutex
	conns map[uint64]net.Conn
}

// Close прекращает держать туннель.
func (l *MeshTunnelListener) Close() {
	if l == nil {
		return
	}
	if l.cancel != nil {
		l.cancel()
	}
	l.closeAll()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
	}
}

// closeAll закрывает локальные соединения стадии.
func (l *MeshTunnelListener) closeAll() {
	l.mu.Lock()
	conns := l.conns
	l.conns = make(map[uint64]net.Conn)
	l.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// StartMeshTunnelListener держит исходящие потоки стадии.
//
// Соединение к локальному RPC-серверу открывается по первому кадру координатора
// с новым идентификатором — столько соединений, сколько открыл RPC.
func StartMeshTunnelListener(ctx context.Context, client *Client, topologyID, selfWorkerID, coordinatorID, localAddr string) (*MeshTunnelListener, error) {
	if client == nil {
		return nil, fmt.Errorf("mesh tunnel: нет клиента платформы")
	}

	linkCtx, cancel := context.WithCancel(ctx)
	listener := &MeshTunnelListener{
		cancel: cancel,
		done:   make(chan struct{}),
		frames: make(chan meshFrame, meshFrameQueue),
		conns:  make(map[uint64]net.Conn),
	}

	upPipe := &meshPipe{
		client:     client,
		topologyID: topologyID,
		stageID:    selfWorkerID,
		to:         coordinatorID,
		frames:     listener.frames,
		log: func() string {
			listener.closeAll()
			return "mesh: поток «вверх» переподключён"
		},
	}
	downPipe := &meshPipe{
		client:     client,
		topologyID: topologyID,
		stageID:    selfWorkerID,
		to:         selfWorkerID,
	}

	go upPipe.runUp(linkCtx)
	go func() {
		defer close(listener.done)
		downPipe.runDown(linkCtx, func(frame meshFrame) error {
			return listener.dispatch(linkCtx, frame, localAddr)
		})
	}()

	return listener, nil
}

// dispatch обслуживает кадр координатора: открывает локальное соединение по
// требованию и перекладывает данные.
func (l *MeshTunnelListener) dispatch(ctx context.Context, frame meshFrame, localAddr string) error {
	l.mu.Lock()
	conn := l.conns[frame.id]
	l.mu.Unlock()

	if frame.kind == meshFrameClose {
		if conn != nil {
			l.mu.Lock()
			delete(l.conns, frame.id)
			l.mu.Unlock()
			return conn.Close()
		}
		return nil
	}
	if frame.kind != meshFrameData {
		return nil
	}

	if conn == nil {
		newConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
		if err != nil {
			return err
		}
		conn = newConn
		l.mu.Lock()
		l.conns[frame.id] = conn
		l.mu.Unlock()
		go l.pumpLocal(ctx, frame.id, conn)
	}

	_, err := conn.Write(frame.payload)
	return err
}

// pumpLocal отправляет в туннель всё, что отвечает локальный RPC-сервер.
func (l *MeshTunnelListener) pumpLocal(ctx context.Context, id uint64, conn net.Conn) {
	buf := make([]byte, meshChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			select {
			case l.frames <- meshFrame{id: id, kind: meshFrameData, payload: payload}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case l.frames <- meshFrame{id: id, kind: meshFrameClose}:
			case <-ctx.Done():
			}
			l.mu.Lock()
			delete(l.conns, id)
			l.mu.Unlock()
			_ = conn.Close()
			return
		}
	}
}
