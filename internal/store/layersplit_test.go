package store

import "testing"

func TestSplitLayersEven(t *testing.T) {
	got := SplitLayers(24, 2)
	want := [][2]int{{0, 11}, {12, 23}}
	if len(got) != len(want) {
		t.Fatalf("кусков %d, нужно %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("кусок %d = %v, нужно %v", index, got[index], want[index])
		}
	}
}

func TestSplitLayersRemainderGoesToTheFirstPieces(t *testing.T) {
	got := SplitLayers(10, 3)
	want := [][2]int{{0, 3}, {4, 6}, {7, 9}}
	if len(got) != len(want) {
		t.Fatalf("кусков %d, нужно %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("кусок %d = %v, нужно %v", index, got[index], want[index])
		}
	}
}

func TestSplitLayersDoesNotExceedLayerCount(t *testing.T) {
	got := SplitLayers(2, 5)
	if len(got) != 2 || got[0] != [2]int{0, 0} || got[1] != [2]int{1, 1} {
		t.Fatalf("раскладка = %v", got)
	}
}

func TestLayerCountKnownModels(t *testing.T) {
	if got := LayerCount("gpt-oss:20b"); got != 24 {
		t.Fatalf("gpt-oss:20b = %d слоёв", got)
	}
	if got := LayerCount("qwen3:8b"); got != 0 {
		t.Fatalf("неизвестная модель не должна резаться, получено %d", got)
	}
}
