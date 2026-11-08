package httpapi

// Туннель шард-группы: байтовый поток между координатором и стадией через
// Control Plane.
//
// Зачем это нужно. `ggml-rpc-server` не умеет ни TLS, ни авторизации, и по
// ADR 0008 его порт не должен смотреть в интернет. При этом стадии стоят за
// домашними NAT, где входящих соединений нет вовсе. Поэтому обе стороны сами
// идут наружу, на уже существующий HTTPS-адрес платформы, а платформа сводит
// их в пару и перекладывает байты.
//
// Почему два односторонних потока, а не один двусторонний. Первый вариант был
// двусторонним: тело запроса — туда, ответ — обратно. Он не работает на
// Go-клиенте: транспорт пишет заголовки запроса в буфер и отправляет их только
// вместе с первым куском тела. Если стороне нечего сказать (а в RPC так бывает
// постоянно — одна сторона ждёт приветствия другой), заголовки не уходят,
// сервер не видит запрос, и обе стороны ждут вечно. Через nginx это маскируется
// (он разбирает заголовки сам), поэтому ошибка воспроизводилась только локально.
//
// Отсюда схема: «вверх» — POST с телом, «вниз» — GET с потоком в ответ. Каждый
// запрос односторонний, буфер между ними маленький и явный.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/private-compute/network/internal/store"
)

const (
	// meshStreamLimit — сколько байт держим в буфере, пока вторая сторона не
	// подключилась. При переполнении поток закрывается, стороны пересоединяются.
	meshStreamLimit = 16 << 20
	// meshStreamIdle — сколько живёт буфер без обращений.
	meshStreamIdle = 2 * time.Minute
	// meshChunk — размер чтения тела запроса.
	meshChunk = 32 << 10
)

// Роли потоков туннеля.
const (
	meshRoleUp   = "up"
	meshRoleDown = "down"
)

// meshStream — односторонний буфер между «вверх» и «вниз».
type meshStream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	used   time.Time
	// ups и downs — сколько потоков сейчас пишут и читают. Когда обоих нет,
	// буфер прошлого сеанса стирается: иначе новый RPC читает чужие кадры и
	// llama.cpp отвечает «Remote RPC server crashed or returned malformed response».
	ups   int
	downs int
}

// newMeshStream создаёт буфер.
func newMeshStream() *meshStream {
	stream := &meshStream{used: time.Now()}
	stream.cond = sync.NewCond(&stream.mu)
	return stream
}

// write добавляет данные от отправителя.
func (s *meshStream) write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	if len(s.buf)+len(p) > meshStreamLimit {
		s.closed = true
		s.cond.Broadcast()
		return errors.New("mesh stream overflow")
	}
	s.buf = append(s.buf, p...)
	s.used = time.Now()
	s.cond.Broadcast()
	return nil
}

// joinUp отмечает нового отправителя. Если пары сейчас нет, прошлые байты
// выбрасываются: их некому было забрать, а новый сеанс примет их за ответ.
func (s *meshStream) joinUp() { s.join(true) }

// joinDown отмечает нового получателя. Правило то же, что у отправителя.
func (s *meshStream) joinDown() { s.join(false) }

// join учитывает сторону. Стирание буфера только когда обе стороны были
// отключены: байты, которые текущий отправитель уже положил, получатель
// ещё должен прочитать.
func (s *meshStream) join(up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ups == 0 && s.downs == 0 {
		s.buf = nil
		s.closed = false
	}
	if up {
		s.ups++
	} else {
		s.downs++
	}
	s.used = time.Now()
}

// leaveUp отмечает, что отправитель отключился.
func (s *meshStream) leaveUp() { s.leave(true) }

// leaveDown отмечает, что получатель отключился.
func (s *meshStream) leaveDown() { s.leave(false) }

// leave стирает буфер, когда сеанс закончился с обеих сторон.
func (s *meshStream) leave(up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if up {
		if s.ups > 0 {
			s.ups--
		}
	} else if s.downs > 0 {
		s.downs--
	}
	if s.ups == 0 && s.downs == 0 {
		s.buf = nil
		s.closed = false
	}
	s.used = time.Now()
	s.cond.Broadcast()
}

// close сообщает читателю, что данных больше не будет.
func (s *meshStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Broadcast()
}

// readTo отдаёт накопленное в w до конца потока или отмены контекста.
func (s *meshStream) readTo(ctx context.Context, w io.Writer, flush func()) {
	// Отмена контекста должна будить ожидание на condition variable.
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()

	for {
		s.mu.Lock()
		for len(s.buf) == 0 && !s.closed && ctx.Err() == nil {
			s.cond.Wait()
		}
		if ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		if len(s.buf) == 0 && s.closed {
			s.mu.Unlock()
			return
		}
		data := s.buf
		s.buf = nil
		s.used = time.Now()
		s.mu.Unlock()

		if _, err := w.Write(data); err != nil {
			return
		}
		if flush != nil {
			flush()
		}
	}
}

// idle сообщает, что буфер давно не использовался.
func (s *meshStream) idle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.used) > meshStreamIdle && len(s.buf) == 0
}

// meshStreamHub держит по буферу на пару «топология + стадия».
type meshStreamHub struct {
	mu      sync.Mutex
	streams map[string]*meshStream
}

var meshStreams = &meshStreamHub{streams: make(map[string]*meshStream)}

// get возвращает буфер пары, создавая его при необходимости.
func (h *meshStreamHub) get(key string) *meshStream {
	h.mu.Lock()
	defer h.mu.Unlock()
	stream, ok := h.streams[key]
	if !ok {
		stream = newMeshStream()
		h.streams[key] = stream
	}
	return stream
}

// sweep убирает давно неиспользуемые буферы.
func (h *meshStreamHub) sweep() {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, stream := range h.streams {
		if stream.idle(now) {
			delete(h.streams, key)
		}
	}
}

// reset закрывает и удаляет буфер пары: нужен, когда соединение оборвалось.
func (h *meshStreamHub) reset(key string) {
	h.mu.Lock()
	stream := h.streams[key]
	delete(h.streams, key)
	h.mu.Unlock()
	if stream != nil {
		stream.close()
	}
}

// registerMeshLinkRoutes подключает туннель шард-группы.
func (cfg Config) registerMeshLinkRoutes(mux *http.ServeMux) {
	handler := cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		topologyID := r.URL.Query().Get("topology")
		stageID := r.URL.Query().Get("stage")
		role := r.Header.Get("X-Mesh-Role")
		if role != meshRoleUp && role != meshRoleDown {
			writeError(w, http.StatusBadRequest, "invalid_request", "X-Mesh-Role must be up or down")
			return
		}
		if topologyID == "" || stageID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "topology and stage are required")
			return
		}

		// Права проверяются по топологии, а не по доверию на слово: стадию
		// открывает только её собственный узел, а координатор — только он сам.
		detail, err := cfg.Store.TopologyByID(r.Context(), topologyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "topology not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot load topology")
			return
		}
		if detail.Topology.Status != "active" && detail.Topology.Status != "staging" {
			writeError(w, http.StatusConflict, "topology_inactive", "topology is not active")
			return
		}
		owners, err := cfg.Store.TopologyOwnerIDs(r.Context(), topologyID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot check topology owners")
			return
		}
		// Чужой компьютер может держать слои в автоматической группе: ведущий
		// там всегда тот, чей чат. Задание при этом забирает только его узел.
		// Ручную группу из разных владельцев туннель не открывает: иначе
		// чужой компьютер можно назначить ведущим чужого чата.
		if len(owners) > 1 && !detail.Topology.Auto {
			writeError(w, http.StatusForbidden, "mixed_owners",
				"a hand-made shard group cannot join machines of different people")
			return
		}
		if !workerInTopology(detail, stageID) {
			writeError(w, http.StatusForbidden, "not_in_topology", "worker is not a stage of this topology")
			return
		}
		isStage := wp.Worker.ID == stageID
		isCoordinator := detail.Topology.CoordinatorID != nil && *detail.Topology.CoordinatorID == wp.Worker.ID
		if !isStage && !isCoordinator {
			writeError(w, http.StatusForbidden, "not_a_party", "worker is neither the stage nor the coordinator")
			return
		}

		// Направление задаётся получателем: у пары два независимых буфера,
		// иначе данные, отправленные одной стороной, вернулись бы ей же.
		toID := r.URL.Query().Get("to")
		if toID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "to is required")
			return
		}
		if toID != stageID && (detail.Topology.CoordinatorID == nil || *detail.Topology.CoordinatorID != toID) {
			writeError(w, http.StatusForbidden, "not_a_party", "to is not a party of this topology")
			return
		}
		// Читать можно только свою очередь: иначе узел забрал бы чужие байты.
		if role == meshRoleDown && toID != wp.Worker.ID {
			writeError(w, http.StatusForbidden, "not_your_queue", "a party may read only its own queue")
			return
		}

		key := topologyID + "|" + stageID + "|" + toID
		meshStreams.sweep()
		stream := meshStreams.get(key)

		switch role {
		case meshRoleUp:
			cfg.pumpUp(w, r, stream, key)
		default:
			cfg.pumpDown(w, r, stream)
		}
	})

	mux.Handle("POST /worker/v1/mesh/link", handler)
	mux.Handle("GET /worker/v1/mesh/link", handler)
}

// pumpUp принимает поток от отправителя и складывает его в буфер пары.
func (cfg Config) pumpUp(w http.ResponseWriter, r *http.Request, stream *meshStream, key string) {
	// Пока получатель жив, буфер не стирается: он ещё не забрал байты.
	// Когда отключаются обе стороны, буфер прошлого сеанса не остаётся
	// следующему — иначе RPC читает чужой кадр и обрывает загрузку.
	stream.joinUp()
	defer stream.leaveUp()
	cfg.Log.Info("mesh stream up", "stage", r.URL.Query().Get("stage"))

	buf := make([]byte, meshChunk)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			if writeErr := stream.write(buf[:n]); writeErr != nil {
				writeError(w, http.StatusInsufficientStorage, "mesh_stream", writeErr.Error())
				return
			}
		}
		if err != nil {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pumpDown отдаёт буфер пары получателю.
func (cfg Config) pumpDown(w http.ResponseWriter, r *http.Request, stream *meshStream) {
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flush()

	cfg.Log.Info("mesh stream down", "stage", r.URL.Query().Get("stage"))
	stream.joinDown()
	defer stream.leaveDown()
	stream.readTo(r.Context(), w, flush)
}

// workerInTopology проверяет, что узел входит в стадии топологии.
func workerInTopology(detail store.TopologyDetail, workerID string) bool {
	for _, stage := range detail.Stages {
		if stage.WorkerID == workerID {
			return true
		}
	}
	return false
}
