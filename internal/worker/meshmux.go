package worker

// Кадры туннеля шард-группы.
//
// Зачем кадры. Один HTTP-поток в каждую сторону — это надёжная доставка байт,
// но не сессии: RPC может открыть несколько соединений (проверка устройств,
// основная сессия) и ждёт, что байты каждого идут цельно. Если просто склеить
// их в один поток, как в первой попытке, получатель разбирает перемешанные
// данные и llama.cpp отвечает «Remote RPC server crashed or returned
// malformed response».
//
// Формат кадра: [uint32 длина][uint64 идентификатор][uint8 тип][данные].
// Длина включает заголовок. Тип 1 — данные, тип 2 — закрытие соединения.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// meshFrameData — кусок данных логического соединения.
	meshFrameData byte = 1
	// meshFrameClose — соединение закрыто, получатель должен закрыть своё.
	meshFrameClose byte = 2
	// meshFrameHeader — длина заголовка кадра.
	meshFrameHeader = 4 + 8 + 1
	// meshFrameMax — предел одного кадра. RPC присылает тензоры крупными
	// кусками, но больше 64 МБ в кадре быть не должно.
	meshFrameMax = 64 << 20
)

// errFrameCorrupt возвращается при нарушении формата.
var errFrameCorrupt = errors.New("mesh frame: corrupt")

// writeMeshFrame записывает один кадр.
func writeMeshFrame(w io.Writer, id uint64, kind byte, payload []byte) error {
	total := meshFrameHeader + len(payload)
	if total > meshFrameMax {
		return fmt.Errorf("mesh frame: too large: %d", total)
	}
	header := make([]byte, meshFrameHeader)
	binary.BigEndian.PutUint32(header[0:4], uint32(total))
	binary.BigEndian.PutUint64(header[4:12], id)
	header[12] = kind
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// readMeshFrames читает кадры до конца потока.
func readMeshFrames(r *bufio.Reader, handle func(id uint64, kind byte, payload []byte) error) error {
	header := make([]byte, meshFrameHeader)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		total := binary.BigEndian.Uint32(header[0:4])
		if total < meshFrameHeader || total > meshFrameMax {
			return fmt.Errorf("%w: length %d", errFrameCorrupt, total)
		}
		id := binary.BigEndian.Uint64(header[4:12])
		kind := header[12]
		size := int(total) - meshFrameHeader

		var payload []byte
		if size > 0 {
			payload = make([]byte, size)
			if _, err := io.ReadFull(r, payload); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return nil
				}
				return err
			}
		}
		if err := handle(id, kind, payload); err != nil {
			return err
		}
	}
}
