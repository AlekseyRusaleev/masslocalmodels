// Package swarm раскладывает веса модели по включённым компьютерам.
//
// Это раздача, а не копия целиком: у каждого участника на диске только часть
// файлов. Одна и та же часть лежит на нескольких компьютерах, поэтому выключенный
// участник не оставляет дыру. Когда раздающих становится меньше, оставшиеся
// докачивают недостающие копии — но только в пределах своего диска. Когда
// людей становится больше, лишние копии удаляются, и только после того как
// другая включённая машина уже держит этот файл.
package swarm

// RetiredModel — пробная модель, которую сеть больше не раздаёт.
// Раньше каждый новый участник скачивал её целиком. Это проверка канала, не продукт.
const RetiredModel = "qwen3:8b"

// DefaultReplicas — сколько включённых компьютеров должны хранить один и тот же кусок.
// Две копии переживают выключение одного участника. Если включённых меньше,
// план честно помечает кусок как недокопированный и отдаёт его всем, у кого есть место.
const DefaultReplicas = 2

// Piece — один файл весов.
type Piece struct {
	ID     string
	Bytes  int64
	Shared bool // общий файл (токенизатор, конфиг): нужен каждому, кто держит слои
}

// Peer — компьютер, который сейчас в сети.
type Peer struct {
	ID     string
	Budget int64    // сколько байт весов можно держать, включая уже лежащие
	Have   []string // имена файлов, которые уже лежат на диске
}

// Options — параметры раскладки.
type Options struct {
	// Replicas — желаемое число копий каждого куска среди включённых.
	// 0 означает DefaultReplicas.
	Replicas int
}

// Decision — что одному компьютеру сделать с диском.
type Decision struct {
	PeerID  string
	Keep    []string
	Fetch   []string
	Release []string
}

// Result — раскладка на текущий состав включённых.
type Result struct {
	Peers []Decision
	// Copies — сколько копий будет у куска, когда все докачают назначенное.
	Copies map[string]int
	// Missing — кусок не лежит ни на одном включённом компьютере и некому его взять.
	Missing []string
	// Short — копий меньше, чем запрошено: раздающих мало или диск кончился.
	Short []string
}

type peerState struct {
	id      string
	budget  int64
	have    map[string]bool
	fetch   map[string]bool
	release map[string]bool
}

// Place назначает каждому включённому компьютеру, какие файлы держать.
//
// Один и тот же набор компьютеров, дисков и уже скачанных файлов даёт одну
// и ту же раскладку. Компьютер не перекачивает файл, который у него уже есть,
// и не удаляет последнюю копию.
func Place(pieces []Piece, peers []Peer, opt Options) Result {
	requested := opt.Replicas
	if requested < 1 {
		requested = DefaultReplicas
	}

	sizes := map[string]int64{}
	dataIDs := map[string]bool{}
	var data []Piece
	var shared []Piece
	for _, piece := range pieces {
		if piece.ID == "" || piece.Bytes < 0 {
			continue
		}
		if _, seen := sizes[piece.ID]; seen {
			continue
		}
		if piece.Bytes == 0 {
			piece.Bytes = 1
		}
		sizes[piece.ID] = piece.Bytes
		if piece.Shared {
			shared = append(shared, piece)
			continue
		}
		data = append(data, piece)
		dataIDs[piece.ID] = true
	}
	sortPieces(data)
	sortPieces(shared)

	states := make([]*peerState, 0, len(peers))
	seenPeer := map[string]bool{}
	for _, peer := range peers {
		if peer.ID == "" || seenPeer[peer.ID] {
			continue
		}
		seenPeer[peer.ID] = true
		state := &peerState{
			id:      peer.ID,
			budget:  peer.Budget,
			have:    map[string]bool{},
			fetch:   map[string]bool{},
			release: map[string]bool{},
		}
		if state.budget < 0 {
			state.budget = 0
		}
		for _, name := range peer.Have {
			if _, ok := sizes[name]; ok {
				state.have[name] = true
			}
		}
		states = append(states, state)
	}
	sortStates(states)

	out := Result{Copies: map[string]int{}}
	if len(states) == 0 {
		for _, piece := range data {
			out.Missing = append(out.Missing, piece.ID)
		}
		for _, piece := range shared {
			out.Missing = append(out.Missing, piece.ID)
		}
		return out
	}

	keep := requested
	if keep > len(states) {
		keep = len(states)
	}

	// Сначала закрываем дыры: кусок с наименьшим числом копий получает
	// следующую копию на компьютер с самым большим свободным местом.
	order := append([]Piece(nil), data...)
	sortByRarity(order, states)
	for _, piece := range order {
		for copiesOf(piece.ID, states) < keep {
			peer := peerWithRoom(piece, states, sizes)
			if peer == nil {
				break
			}
			peer.fetch[piece.ID] = true
		}
	}

	fair := fairShare(data, len(states), keep)
	rebalance(data, states, sizes, dataIDs, fair)
	releaseExtras(data, states, sizes, dataIDs, fair, keep)
	assignShared(shared, states, sizes, dataIDs)

	for _, piece := range data {
		copies := copiesOf(piece.ID, states)
		out.Copies[piece.ID] = copies
		switch {
		case copies == 0:
			out.Missing = append(out.Missing, piece.ID)
		case copies < requested:
			out.Short = append(out.Short, piece.ID)
		}
	}
	for _, piece := range shared {
		copies := copiesOf(piece.ID, states)
		out.Copies[piece.ID] = copies
		if copies == 0 && len(holdersOfData(states, dataIDs)) > 0 {
			out.Missing = append(out.Missing, piece.ID)
		}
	}

	out.Peers = make([]Decision, 0, len(states))
	for _, state := range states {
		decision := Decision{PeerID: state.id}
		for name := range state.have {
			if !state.release[name] {
				decision.Keep = append(decision.Keep, name)
			}
		}
		for name := range state.fetch {
			decision.Fetch = append(decision.Fetch, name)
			decision.Keep = append(decision.Keep, name)
		}
		for name := range state.release {
			decision.Release = append(decision.Release, name)
		}
		sortStrings(decision.Keep)
		sortStrings(decision.Fetch)
		sortStrings(decision.Release)
		out.Peers = append(out.Peers, decision)
	}
	return out
}

// DecisionFor возвращает раскладку одного компьютера.
func DecisionFor(result Result, peerID string) (Decision, bool) {
	for _, decision := range result.Peers {
		if decision.PeerID == peerID {
			return decision, true
		}
	}
	return Decision{}, false
}

func rebalance(pieces []Piece, states []*peerState, sizes map[string]int64, dataIDs map[string]bool, fair int64) {
	limit := len(pieces)*len(states) + 1
	for step := 0; step < limit; step++ {
		var fat *peerState
		var fatBytes int64
		for _, state := range states {
			held := haveBytes(state, sizes, dataIDs)
			if held <= fair {
				continue
			}
			if fat == nil || held > fatBytes || (held == fatBytes && state.id < fat.id) {
				fat = state
				fatBytes = held
			}
		}
		if fat == nil {
			return
		}
		var chosen Piece
		var thin *peerState
		for _, piece := range pieces {
			if !fat.have[piece.ID] {
				continue
			}
			for _, state := range states {
				if state == fat || state.holds(piece.ID) {
					continue
				}
				if state.budget-plannedBytes(state, sizes) < piece.Bytes {
					continue
				}
				if plannedBytes(state, sizes) >= fair {
					continue
				}
				if thin == nil || piece.Bytes > chosen.Bytes ||
					(piece.Bytes == chosen.Bytes && state.id < thin.id) {
					chosen = piece
					thin = state
				}
			}
		}
		if thin == nil {
			return
		}
		thin.fetch[chosen.ID] = true
	}
}

func releaseExtras(pieces []Piece, states []*peerState, sizes map[string]int64, dataIDs map[string]bool, fair int64, keep int) {
	live := map[string]int{}
	for _, piece := range pieces {
		for _, state := range states {
			if state.have[piece.ID] {
				live[piece.ID]++
			}
		}
	}
	for _, state := range states {
		for haveBytes(state, sizes, dataIDs) > fair {
			var drop Piece
			found := false
			for _, piece := range pieces {
				if !state.have[piece.ID] || state.release[piece.ID] {
					continue
				}
				if live[piece.ID]-1 < keep {
					continue
				}
				if !found || piece.Bytes > drop.Bytes || (piece.Bytes == drop.Bytes && piece.ID < drop.ID) {
					drop = piece
					found = true
				}
			}
			if !found {
				break
			}
			state.release[drop.ID] = true
			live[drop.ID]--
		}
	}
}

func assignShared(shared []Piece, states []*peerState, sizes map[string]int64, dataIDs map[string]bool) {
	holders := holdersOfData(states, dataIDs)
	if len(holders) == 0 {
		return
	}
	for _, piece := range shared {
		for _, state := range states {
			if !holders[state.id] || state.holds(piece.ID) {
				continue
			}
			if state.budget-plannedBytes(state, sizes) < piece.Bytes {
				continue
			}
			state.fetch[piece.ID] = true
		}
	}
}

func holdersOfData(states []*peerState, dataIDs map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, state := range states {
		for name := range state.have {
			if dataIDs[name] && !state.release[name] {
				out[state.id] = true
			}
		}
		for name := range state.fetch {
			if dataIDs[name] {
				out[state.id] = true
			}
		}
	}
	return out
}

func peerWithRoom(piece Piece, states []*peerState, sizes map[string]int64) *peerState {
	var best *peerState
	var bestFree int64
	for _, state := range states {
		if state.holds(piece.ID) {
			continue
		}
		free := state.budget - plannedBytes(state, sizes)
		if free < piece.Bytes {
			continue
		}
		if best == nil || free > bestFree || (free == bestFree && state.id < best.id) {
			best = state
			bestFree = free
		}
	}
	return best
}

func (p *peerState) holds(id string) bool {
	return (p.have[id] && !p.release[id]) || p.fetch[id]
}

func copiesOf(id string, states []*peerState) int {
	copies := 0
	for _, state := range states {
		if state.holds(id) {
			copies++
		}
	}
	return copies
}

func haveBytes(state *peerState, sizes map[string]int64, only map[string]bool) int64 {
	var total int64
	for name := range state.have {
		if state.release[name] {
			continue
		}
		if only != nil && !only[name] {
			continue
		}
		total += sizes[name]
	}
	return total
}

func plannedBytes(state *peerState, sizes map[string]int64) int64 {
	total := haveBytes(state, sizes, nil)
	for name := range state.fetch {
		total += sizes[name]
	}
	return total
}

func fairShare(pieces []Piece, peers, copies int) int64 {
	if peers < 1 || copies < 1 {
		return 0
	}
	var total int64
	for _, piece := range pieces {
		total += piece.Bytes
	}
	stored := total * int64(copies)
	return (stored + int64(peers) - 1) / int64(peers)
}

func sortByRarity(pieces []Piece, states []*peerState) {
	for i := 1; i < len(pieces); i++ {
		item := pieces[i]
		j := i
		for j > 0 {
			prev := pieces[j-1]
			prevCopies := copiesOf(prev.ID, states)
			itemCopies := copiesOf(item.ID, states)
			if prevCopies < itemCopies || (prevCopies == itemCopies && prev.ID <= item.ID) {
				break
			}
			pieces[j] = prev
			j--
		}
		pieces[j] = item
	}
}

func sortPieces(pieces []Piece) {
	for i := 1; i < len(pieces); i++ {
		item := pieces[i]
		j := i
		for j > 0 && pieces[j-1].ID > item.ID {
			pieces[j] = pieces[j-1]
			j--
		}
		pieces[j] = item
	}
}

func sortStates(states []*peerState) {
	for i := 1; i < len(states); i++ {
		item := states[i]
		j := i
		for j > 0 && states[j-1].id > item.id {
			states[j] = states[j-1]
			j--
		}
		states[j] = item
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		item := values[i]
		j := i
		for j > 0 && values[j-1] > item {
			values[j] = values[j-1]
			j--
		}
		values[j] = item
	}
}
