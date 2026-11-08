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

func TestForeignLayerOrdersEachOwnerLeadsTheirOwnChat(t *testing.T) {
	orders := foreignLayerOrders([]meshParticipant{
		{ID: "b", Owner: "two"},
		{ID: "a", Owner: "one"},
		{ID: "c", Owner: "one"},
	})
	if len(orders) != 2 {
		t.Fatalf("групп %d, нужно 2", len(orders))
	}
	// Владелец one идёт первым по имени, его компьютеры отсортированы.
	if orders[0][0] != "a" || orders[0][1] != "c" || orders[0][2] != "b" {
		t.Fatalf("группа первого владельца = %v", orders[0])
	}
	if orders[1][0] != "b" || len(orders[1]) != 3 {
		t.Fatalf("группа второго владельца = %v", orders[1])
	}
}

func TestForeignLayerOrdersNeedsTwoComputers(t *testing.T) {
	if got := foreignLayerOrders([]meshParticipant{{ID: "a", Owner: "one"}}); got != nil {
		t.Fatalf("одного компьютера недостаточно, получено %v", got)
	}
	own := foreignLayerOrders([]meshParticipant{
		{ID: "b", Owner: "one"},
		{ID: "a", Owner: "one"},
	})
	if len(own) != 1 || own[0][0] != "a" || own[0][1] != "b" {
		t.Fatalf("две свои машины = %v", own)
	}
}

func TestSplitLayersByCapacityFollowsVRAM(t *testing.T) {
	got := SplitLayersByCapacity(80, []uint64{8 << 30, 16 << 30, 16 << 30})
	if len(got) != 3 {
		t.Fatalf("кусков %d, нужно 3: %v", len(got), got)
	}
	if got[0][0] != 0 {
		t.Fatalf("первый кусок должен начинаться с 0: %v", got)
	}
	if got[2][1] != 79 {
		t.Fatalf("последний слой должен быть 79: %v", got)
	}
	first := got[0][1] - got[0][0] + 1
	second := got[1][1] - got[1][0] + 1
	third := got[2][1] - got[2][0] + 1
	if first+second+third != 80 {
		t.Fatalf("слоёв %d, нужно 80: %v", first+second+third, got)
	}
	// Карта 8 ГБ не должна получать столько же, сколько 16 ГБ: иначе ведущий
	// снова просит ~14 ГБ на карту, которой это некуда положить.
	if first >= second || first >= third {
		t.Fatalf("маленькая карта получила не меньше большой: %v", got)
	}
	if first > 20 {
		t.Fatalf("ведущему на 8 ГБ ушло %d слоёв из 80, это снова треть модели: %v", first, got)
	}
}

func TestSplitLayersByCapacityEqualCardsMatchEqualSplit(t *testing.T) {
	got := SplitLayersByCapacity(24, []uint64{8 << 30, 8 << 30})
	want := SplitLayers(24, 2)
	if len(got) != len(want) {
		t.Fatalf("кусков %d, нужно %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("кусок %d = %v, нужно %v", index, got[index], want[index])
		}
	}
}

func TestSplitLayersByCapacityUnknownFallsBackToEqual(t *testing.T) {
	got := SplitLayersByCapacity(24, []uint64{8 << 30, 0})
	want := SplitLayers(24, 2)
	if len(got) != len(want) {
		t.Fatalf("кусков %d, нужно %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("без размера карты кусок %d = %v, нужно равный %v", index, got[index], want[index])
		}
	}
}

func TestLayerCountKnownModels(t *testing.T) {
	if got := LayerCount("gpt-oss:20b"); got != 24 {
		t.Fatalf("gpt-oss:20b = %d слоёв", got)
	}
	if got := LayerCount("qwen2.5:72b-instruct-q4_0"); got != 80 {
		t.Fatalf("qwen2.5:72b-instruct-q4_0 = %d слоёв", got)
	}
	if got := LayerCount("qwen3:8b"); got != 0 {
		t.Fatalf("неизвестная модель не должна резаться, получено %d", got)
	}
}
