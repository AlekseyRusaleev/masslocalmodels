package worker

import "testing"

func TestModelPullActiveWhileBytesRemain(t *testing.T) {
	if !modelPullActive(4<<30, 38<<30) {
		t.Fatal("недописанный файл должен двигать полосу")
	}
	if modelPullActive(38<<30, 38<<30) {
		t.Fatal("дописанный файл не должен держать полосу")
	}
	if modelPullActive(100, 200) {
		t.Fatal("мелкий файл не является весами")
	}
}
