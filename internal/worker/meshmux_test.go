package worker

import (
	"bufio"
	"bytes"
	"testing"
)

// Кадры должны переживать запись-чтение без потерь и путаницы соединений:
// именно перемешивание двух сессий ломало llama.cpp в первой версии туннеля.
func TestMeshFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frames := []meshFrame{
		{id: 1, kind: meshFrameData, payload: []byte("hello")},
		{id: 2, kind: meshFrameData, payload: []byte("world")},
		{id: 1, kind: meshFrameData, payload: []byte("second")},
		{id: 2, kind: meshFrameClose},
		{id: 1, kind: meshFrameClose},
	}
	for _, frame := range frames {
		if err := writeMeshFrame(&buf, frame.id, frame.kind, frame.payload); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	var got []meshFrame
	err := readMeshFrames(bufio.NewReader(&buf), func(id uint64, kind byte, payload []byte) error {
		copied := make([]byte, len(payload))
		copy(copied, payload)
		got = append(got, meshFrame{id: id, kind: kind, payload: copied})
		return nil
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(frames) {
		t.Fatalf("кадров %d, ожидалось %d", len(got), len(frames))
	}
	for index, want := range frames {
		if got[index].id != want.id || got[index].kind != want.kind ||
			string(got[index].payload) != string(want.payload) {
			t.Fatalf("кадр %d = %+v, ожидался %+v", index, got[index], want)
		}
	}
}

// Битая длина кадра — это ошибка, а не повод читать дальше: иначе получатель
// уйдёт в бесконечное чтение мусора.
func TestMeshFrameRejectsBadLength(t *testing.T) {
	header := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, meshFrameData}
	err := readMeshFrames(bufio.NewReader(bytes.NewReader(header)), func(uint64, byte, []byte) error {
		return nil
	})
	if err == nil {
		t.Fatal("ожидалась ошибка формата")
	}
}
