package swarm

import "testing"

func pieces(names ...string) []Piece {
	out := make([]Piece, 0, len(names))
	for _, name := range names {
		out = append(out, Piece{ID: name, Bytes: 10})
	}
	return out
}

func peer(id string, budget int64, have ...string) Peer {
	return Peer{ID: id, Budget: budget, Have: have}
}

func decision(result Result, id string) Decision {
	item, ok := DecisionFor(result, id)
	if !ok {
		return Decision{}
	}
	return item
}

func TestPlaceSpreadsTwoCopies(t *testing.T) {
	result := Place(pieces("a", "b", "c", "d"), []Peer{
		peer("p1", 100),
		peer("p2", 100),
		peer("p3", 100),
		peer("p4", 100),
	}, Options{Replicas: 2})

	if len(result.Missing) != 0 || len(result.Short) != 0 {
		t.Fatalf("дыры: missing=%v short=%v", result.Missing, result.Short)
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		if result.Copies[name] != 2 {
			t.Fatalf("кусок %s имеет %d копий, нужно 2", name, result.Copies[name])
		}
	}
	for _, item := range result.Peers {
		if len(item.Keep) != 2 {
			t.Fatalf("%s держит %v, ожидалась равная доля из двух кусков", item.PeerID, item.Keep)
		}
		if len(item.Release) != 0 {
			t.Fatalf("%s удаляет %v на пустом диске", item.PeerID, item.Release)
		}
	}
}

func TestPlaceSameInputSameResult(t *testing.T) {
	peers := []Peer{peer("b", 50), peer("a", 50)}
	first := Place(pieces("x", "y"), peers, Options{Replicas: 2})
	second := Place(pieces("y", "x"), []Peer{peer("a", 50), peer("b", 50)}, Options{})
	if len(first.Peers) != len(second.Peers) {
		t.Fatalf("разная длина: %#v и %#v", first.Peers, second.Peers)
	}
	for i := range first.Peers {
		if first.Peers[i].PeerID != second.Peers[i].PeerID {
			t.Fatalf("порядок %s против %s", first.Peers[i].PeerID, second.Peers[i].PeerID)
		}
		if join(first.Peers[i].Fetch) != join(second.Peers[i].Fetch) {
			t.Fatalf("разная закачка у %s: %v против %v", first.Peers[i].PeerID, first.Peers[i].Fetch, second.Peers[i].Fetch)
		}
	}
}

func TestPlaceKeepsFileAlreadyOnDisk(t *testing.T) {
	result := Place(pieces("a", "b"), []Peer{
		peer("p1", 40, "a"),
		peer("p2", 40),
	}, Options{Replicas: 2})
	if contains(decision(result, "p1").Fetch, "a") {
		t.Fatalf("p1 заново качает файл, который уже лежит: %+v", decision(result, "p1"))
	}
	if !contains(decision(result, "p1").Keep, "a") {
		t.Fatalf("p1 не оставляет a: %+v", decision(result, "p1"))
	}
}

func TestPlaceWhenSomeoneTurnsOff(t *testing.T) {
	files := pieces("a", "b", "c")
	first := Place(files, []Peer{
		peer("a", 60),
		peer("b", 60),
		peer("c", 60),
	}, Options{Replicas: 2})
	held := map[string][]string{}
	for _, item := range first.Peers {
		held[item.PeerID] = append([]string(nil), item.Keep...)
	}

	// c выключился. Его копии больше не считаются, оставшиеся докачивают дыры.
	second := Place(files, []Peer{
		peer("a", 60, held["a"]...),
		peer("b", 60, held["b"]...),
	}, Options{Replicas: 2})
	for _, name := range []string{"a", "b", "c"} {
		if second.Copies[name] != 2 {
			t.Fatalf("после ухода c кусок %s имеет %d копий", name, second.Copies[name])
		}
	}
	for _, item := range second.Peers {
		for _, name := range item.Release {
			if second.Copies[name] < 2 {
				t.Fatalf("%s удаляет %s, после этого копий %d", item.PeerID, name, second.Copies[name])
			}
		}
	}
}

func TestPlaceNewPeerTakesWeightBeforeOldOnesDelete(t *testing.T) {
	files := pieces("a", "b")
	// Оба держат всё. Приходит третий — ему назначают закачку,
	// а старые ещё не стирают файл, пока у нового его нет на диске.
	before := Place(files, []Peer{
		peer("a", 40, "a", "b"),
		peer("b", 40, "a", "b"),
		peer("c", 40),
	}, Options{Replicas: 2})
	if len(decision(before, "c").Fetch) == 0 {
		t.Fatalf("новый участник ничего не получил: %+v", before.Peers)
	}
	for _, id := range []string{"a", "b"} {
		if len(decision(before, id).Release) != 0 {
			t.Fatalf("%s стирает %v до того, как новый участник скачал", id, decision(before, id).Release)
		}
	}

	// Новый участник скачал. Теперь лишние копии можно отпустить.
	after := Place(files, []Peer{
		peer("a", 40, "a", "b"),
		peer("b", 40, "a", "b"),
		peer("c", 40, decision(before, "c").Keep...),
	}, Options{Replicas: 2})
	released := len(decision(after, "a").Release) + len(decision(after, "b").Release)
	if released == 0 {
		t.Fatalf("после появления третьего никто не отдал лишнее: %+v", after.Peers)
	}
	for _, name := range []string{"a", "b"} {
		if after.Copies[name] < 2 {
			t.Fatalf("кусок %s остался в %d копиях", name, after.Copies[name])
		}
	}
}

func TestPlaceOneComputerHoldsWhatFits(t *testing.T) {
	result := Place(pieces("a", "b", "c"), []Peer{peer("solo", 15)}, Options{Replicas: 2})
	item := decision(result, "solo")
	if len(item.Keep) != 1 {
		t.Fatalf("в бюджет 15 влез не один кусок: %+v", item)
	}
	if len(result.Missing) != 2 {
		t.Fatalf("непоместившиеся куски: %v", result.Missing)
	}
	if len(result.Short) != 1 {
		t.Fatalf("единственная копия должна быть помечена как неполная: short=%v", result.Short)
	}
}

func TestPlaceDoesNotDeleteLastCopy(t *testing.T) {
	result := Place(pieces("only"), []Peer{
		peer("a", 5, "only"),
		peer("b", 100),
	}, Options{Replicas: 1})
	// Бюджет a меньше файла, но файл уже лежит и больше его ни у кого нет,
	// пока b не скачает. Стирать нельзя.
	if contains(decision(result, "a").Release, "only") {
		t.Fatalf("стёрта единственная копия: %+v", decision(result, "a"))
	}
	if !contains(decision(result, "b").Fetch, "only") {
		t.Fatalf("b не получил единственный кусок: %+v", decision(result, "b"))
	}
}

func TestPlaceSharedFileFollowsLayerHolders(t *testing.T) {
	result := Place([]Piece{
		{ID: "layers", Bytes: 10},
		{ID: "tokenizer", Bytes: 1, Shared: true},
	}, []Peer{
		peer("a", 20),
		peer("b", 20),
	}, Options{Replicas: 2})
	for _, id := range []string{"a", "b"} {
		if !contains(decision(result, id).Keep, "tokenizer") {
			t.Fatalf("%s не получил общий файл: %+v", id, decision(result, id))
		}
	}
}

func TestPlaceNobodyOnline(t *testing.T) {
	result := Place(pieces("a"), nil, Options{Replicas: 2})
	if len(result.Missing) != 1 || result.Missing[0] != "a" {
		t.Fatalf("при пустой сети кусок не отмечен пропавшим: %+v", result)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func join(values []string) string {
	out := ""
	for _, value := range values {
		out += value + ","
	}
	return out
}
