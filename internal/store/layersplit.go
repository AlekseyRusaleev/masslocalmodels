package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LayerCount — сколько блоков у модели, которую платформа умеет резать.
//
// Ноль означает «не режем»: лучше оставить реплику на одном узле, чем
// выдумать число слоёв и раздать пустые диапазоны.
func LayerCount(model string) int {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-oss:20b", "gpt-oss:20b:latest":
		return 24
	case "qwen2.5:72b-instruct-q4_0", "qwen2.5:72b-instruct-q4_0:latest":
		// Qwen2.5-72B: num_hidden_layers в конфигурации модели равен 80.
		return 80
	default:
		return 0
	}
}

// SplitLayers делит слои 0..total-1 на parts непрерывных кусков.
//
// Лишние слои получают первые куски, поэтому диапазоны отличаются не больше
// чем на один слой. Кусков не больше, чем слоёв. Это запасной путь, когда
// размер карт неизвестен. Обычная автоматическая группа режет по VRAM.
func SplitLayers(total, parts int) [][2]int {
	if total <= 0 || parts <= 0 {
		return nil
	}
	if parts > total {
		parts = total
	}
	base := total / parts
	extra := total % parts
	out := make([][2]int, 0, parts)
	start := 0
	for index := 0; index < parts; index++ {
		count := base
		if index < extra {
			count++
		}
		out = append(out, [2]int{start, start + count - 1})
		start += count
	}
	return out
}

// vramSplitReserve — запас на рабочий стол и KV-кэш, который нельзя отдать
// под веса. На карте 8 ГБ без этого запаса ведущий получает треть 72B и
// просит у карты больше, чем на ней есть.
const vramSplitReserve = uint64(3) << 29 // 1.5 ГиБ

// SplitLayersByCapacity делит слои пропорционально объёму карт, а не поровну.
//
// Смысл сети — держать модель, которая не влезает на одну карту. Поровну
// 80 слоёв на карту 8 ГБ и карту 16 ГБ даёт ведущему ~14 ГБ весов. Карта
// этого не принимает. Если объём хотя бы одной карты неизвестен, раскладка
// остаётся равной: лучше одинаковые куски, чем выдуманные гигабайты.
func SplitLayersByCapacity(total int, capacities []uint64) [][2]int {
	n := len(capacities)
	if total <= 0 || n <= 0 {
		return nil
	}
	if n > total {
		capacities = capacities[:total]
		n = total
	}
	for _, cap := range capacities {
		if cap == 0 {
			return SplitLayers(total, n)
		}
	}
	weights := make([]uint64, n)
	var sum uint64
	for index, cap := range capacities {
		weight := vramForSplit(cap)
		if weight == 0 {
			weight = 1
		}
		weights[index] = weight
		sum += weight
	}
	if sum == 0 {
		return SplitLayers(total, n)
	}
	counts := make([]int, n)
	type remainder struct {
		index int
		frac  uint64
	}
	remainders := make([]remainder, n)
	assigned := 0
	for index := 0; index < n; index++ {
		prod := uint64(total) * weights[index]
		counts[index] = int(prod / sum)
		remainders[index] = remainder{index: index, frac: prod % sum}
		assigned += counts[index]
	}
	for index := 0; index < n && assigned < total; index++ {
		if counts[index] == 0 {
			counts[index] = 1
			assigned++
		}
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].frac == remainders[j].frac {
			return remainders[i].index < remainders[j].index
		}
		return remainders[i].frac > remainders[j].frac
	})
	for assigned < total {
		progress := false
		for _, item := range remainders {
			if assigned >= total {
				break
			}
			counts[item.index]++
			assigned++
			progress = true
		}
		if !progress {
			break
		}
	}
	for assigned > total {
		reduced := false
		for index := n - 1; index >= 0; index-- {
			if counts[index] > 1 {
				counts[index]--
				assigned--
				reduced = true
				break
			}
		}
		if !reduced {
			break
		}
	}
	return countsToRanges(counts)
}

func vramForSplit(total uint64) uint64 {
	if total <= vramSplitReserve {
		return 1
	}
	return total - vramSplitReserve
}

func countsToRanges(counts []int) [][2]int {
	out := make([][2]int, 0, len(counts))
	start := 0
	for _, count := range counts {
		if count <= 0 {
			continue
		}
		out = append(out, [2]int{start, start + count - 1})
		start += count
	}
	return out
}

// EnsureLayerSplit держит автоматическую раскладку слоёв в актуальном виде.
//
// Обычный путь — группа слоёв. Узел, который сам сообщил model_fits, в группу
// не входит и считает свои запросы целиком. Остальные узлы от этого не
// распускаются. Меньше двух участников снимает только автоматические
// раскладки. Ручная топология не трогается.
//
// На каждого владельца собирается своя группа, если в сети есть ещё хотя бы
// один компьютер. Первым в ней стоит его собственный компьютер: он забирает
// его чат. Чужие компьютеры идут следом и только держат слои. Общий аккаунт
// для этого не нужен.
func (s *Store) EnsureLayerSplit(ctx context.Context, model string) error {
	layers := LayerCount(model)
	if layers == 0 {
		return nil
	}
	model = strings.TrimSpace(model)

	// Размер модели: сначала поле ревизии, а если оно не заполнено — сумма
	// размеров файлов из каталога. Без этого проверка влезания молчала бы
	// ровно у тех моделей, у которых поля нет.
	var revisionID string
	var modelBytes int64
	err := s.pool.QueryRow(ctx, `
		SELECT r.id::text,
		       GREATEST(
		         COALESCE(r.size_bytes, 0),
		         COALESCE((SELECT SUM(a.size_bytes) FROM model_artifacts a
		                    WHERE a.revision_id = r.id), 0)
		       )
		FROM model_revisions r
		JOIN model_catalog m ON m.id = r.model_id
		WHERE lower(m.name) = lower($1) AND r.status = 'active'
		ORDER BY r.created_at DESC
		LIMIT 1`, model).Scan(&revisionID, &modelBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: revision for split: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (h.worker_id) h.worker_id::text,
		       coalesce(w.owner_user_id::text, ''),
		       coalesce((
		         SELECT SUM((elem->>'vram_bytes')::bigint)
		         FROM jsonb_array_elements(coalesce(c.gpu_json, '[]'::jsonb)) elem
		         WHERE coalesce(elem->>'vram_bytes', '') ~ '^[0-9]+$'
		       ), 0)
		FROM worker_heartbeats h
		JOIN worker_nodes w ON w.id = h.worker_id
		LEFT JOIN worker_capabilities c ON c.worker_id = h.worker_id
		WHERE h.observed_at > now() - interval '2 minutes'
		  AND w.state = 'available'
		  AND h.meta_json->>'mesh_capable' = 'true'
		  AND coalesce(h.meta_json->>'model_fits', '') <> 'true'
		  AND coalesce(w.owner_user_id::text, '') <> ''
		  AND lower(h.meta_json->>'mesh_model') = lower($1)
		ORDER BY h.worker_id, h.observed_at DESC`, model)
	if err != nil {
		return fmt.Errorf("store: mesh workers: %w", err)
	}
	defer rows.Close()

	var members []meshParticipant
	vram := map[string]uint64{}
	for rows.Next() {
		var id, owner string
		var card int64
		if err := rows.Scan(&id, &owner, &card); err != nil {
			return fmt.Errorf("store: scan mesh worker: %w", err)
		}
		members = append(members, meshParticipant{ID: id, Owner: owner})
		if card > 0 {
			vram[id] = uint64(card)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: mesh workers: %w", err)
	}
	orders := foreignLayerOrders(members)

	// Проверка влезания: если модель не помещается в сумму видеопамяти всех
	// участников, группа заведомо не поднимется. Вместо того чтобы выдать
	// раскладку и ждать `cudaMalloc failed`, записываем отказ с числами.
	// KV-кэш на этом уровне неизвестен, поэтому оценка снизу — только веса;
	// если она не проходит, дальше проверять нечего.
	caps := make([]uint64, 0, len(members))
	for _, member := range members {
		caps = append(caps, vram[member.ID])
	}
	if fit := PlanGroupFit(layers, uint64(modelBytes), 0, 0, caps); fit.Known && !fit.Fits {
		return s.recordSplitRefusal(ctx, revisionID, model, fit)
	}

	active, err := s.activeAutoSplits(ctx, revisionID)
	if err != nil {
		return err
	}
	drained := map[string]bool{}
	for _, split := range active {
		if orderMatches(orders, vram, layers, split) {
			continue
		}
		if err := s.drainTopology(ctx, split.id); err != nil {
			return err
		}
		drained[split.id] = true
	}
	for _, order := range orders {
		want := expectedLayerRanges(layers, order, vram)
		if orderAlive(active, drained, order, want) {
			continue
		}
		if err := s.createAutoSplit(ctx, revisionID, layers, order, want); err != nil {
			return err
		}
	}
	return nil
}

// meshParticipant — компьютер, который может держать слои, и его владелец.
type meshParticipant struct {
	ID    string
	Owner string
}

// foreignLayerOrders строит порядок компьютеров в группе каждого владельца.
//
// Первым стоит его собственный компьютер, дальше — чужие. У группы меньше
// двух компьютеров не бывает: одному нечего отдавать. Владелец без
// компьютера в список не попадает.
func foreignLayerOrders(members []meshParticipant) [][]string {
	byOwner := map[string][]string{}
	var owners []string
	total := 0
	for _, member := range members {
		if member.ID == "" || member.Owner == "" {
			continue
		}
		if _, seen := byOwner[member.Owner]; !seen {
			owners = append(owners, member.Owner)
		}
		byOwner[member.Owner] = append(byOwner[member.Owner], member.ID)
		total++
	}
	if total < 2 {
		return nil
	}
	sort.Strings(owners)
	for _, owner := range owners {
		sort.Strings(byOwner[owner])
	}
	orders := make([][]string, 0, len(owners))
	for _, owner := range owners {
		order := append([]string{}, byOwner[owner]...)
		for _, other := range owners {
			if other == owner {
				continue
			}
			order = append(order, byOwner[other]...)
		}
		if len(order) < 2 {
			continue
		}
		orders = append(orders, order)
	}
	return orders
}

func expectedLayerRanges(layers int, order []string, vram map[string]uint64) [][2]int {
	caps := make([]uint64, len(order))
	for index, id := range order {
		caps[index] = vram[id]
	}
	return SplitLayersByCapacity(layers, caps)
}

func orderMatches(orders [][]string, vram map[string]uint64, layers int, split autoSplit) bool {
	for _, order := range orders {
		if splitMatches(order, expectedLayerRanges(layers, order, vram), split) {
			return true
		}
	}
	return false
}

func orderAlive(splits []autoSplit, drained map[string]bool, order []string, want [][2]int) bool {
	for _, split := range splits {
		if drained[split.id] {
			continue
		}
		if splitMatches(order, want, split) {
			return true
		}
	}
	return false
}

func splitMatches(order []string, want [][2]int, split autoSplit) bool {
	if len(order) == 0 || split.coordinator != order[0] {
		return false
	}
	set := append([]string{}, order...)
	sort.Strings(set)
	if !sameIDs(set, split.workers) {
		return false
	}
	if len(want) != len(split.ranges) {
		return false
	}
	for index := range want {
		if want[index] != split.ranges[index] {
			return false
		}
	}
	return true
}

type autoSplit struct {
	id          string
	coordinator string
	workers     []string
	ranges      [][2]int
}

func (s *Store) activeAutoSplits(ctx context.Context, revisionID string) ([]autoSplit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, coalesce(coordinator_worker_id::text, '')
		FROM topologies
		WHERE revision_id = $1::uuid AND status = 'active' AND auto
		ORDER BY id`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("store: active auto splits: %w", err)
	}
	defer rows.Close()
	var splits []autoSplit
	for rows.Next() {
		var split autoSplit
		if err := rows.Scan(&split.id, &split.coordinator); err != nil {
			return nil, fmt.Errorf("store: scan auto split: %w", err)
		}
		workers, err := s.topologyWorkers(ctx, split.id)
		if err != nil {
			return nil, err
		}
		split.workers = workers
		ranges, err := s.topologyStageRanges(ctx, split.id)
		if err != nil {
			return nil, err
		}
		split.ranges = ranges
		splits = append(splits, split)
	}
	return splits, rows.Err()
}

func (s *Store) topologyStageRanges(ctx context.Context, topologyID string) ([][2]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT layer_start, layer_end
		FROM topology_stages
		WHERE topology_id = $1::uuid
		ORDER BY stage_index`, topologyID)
	if err != nil {
		return nil, fmt.Errorf("store: topology ranges: %w", err)
	}
	defer rows.Close()
	var ranges [][2]int
	for rows.Next() {
		var start, end int
		if err := rows.Scan(&start, &end); err != nil {
			return nil, fmt.Errorf("store: scan topology range: %w", err)
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges, rows.Err()
}

func (s *Store) topologyWorkers(ctx context.Context, topologyID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT worker_id::text FROM topology_stages
		WHERE topology_id = $1::uuid ORDER BY worker_id`, topologyID)
	if err != nil {
		return nil, fmt.Errorf("store: topology workers: %w", err)
	}
	defer rows.Close()
	var workers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan topology worker: %w", err)
		}
		workers = append(workers, id)
	}
	return workers, rows.Err()
}

func (s *Store) createAutoSplit(ctx context.Context, revisionID string, layers int, workers []string, ranges [][2]int) error {
	if len(ranges) != len(workers) {
		ranges = SplitLayers(layers, len(workers))
	}
	stages := make([]StageInput, len(workers))
	for index, id := range workers {
		role := "compute"
		if index == 0 {
			role = "coordinate"
		}
		width := ranges[index][1] - ranges[index][0] + 1
		stages[index] = StageInput{
			WorkerID:   id,
			LayerStart: ranges[index][0],
			LayerEnd:   ranges[index][1],
			Share:      float32(width) / float32(layers),
			Role:       role,
		}
	}
	detail, err := s.CreateTopology(ctx, revisionID, workers[0], "layer", stages)
	if err != nil {
		return err
	}
	// Активация не снимает остальные автоматические группы этой же модели:
	// у каждого владельца своя, и ведущий в ней свой. Обычная смена статуса
	// через панель по-прежнему оставляет активной только одну топологию.
	if _, err := s.pool.Exec(ctx, `
		UPDATE topologies
		SET auto = true, status = 'active', activated_at = COALESCE(activated_at, now())
		WHERE id = $1::uuid`, detail.Topology.ID); err != nil {
		return fmt.Errorf("store: activate auto topology: %w", err)
	}
	return nil
}

func (s *Store) drainTopology(ctx context.Context, topologyID string) error {
	if err := s.SetTopologyStatus(ctx, topologyID, "draining"); err != nil {
		return fmt.Errorf("store: drain auto split: %w", err)
	}
	return nil
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// recordSplitRefusal записывает, почему автоматическая группа не создана.
//
// Прежние автоматические группы этой ревизии переводятся в draining: они были
// собраны по тем же узлам и работать не могут. Запись идемпотентна — при
// повторном вызове с тем же текстом новая строка не появляется.
func (s *Store) recordSplitRefusal(ctx context.Context, revisionID, model string, fit GroupFit) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE topologies SET status = 'draining'
		 WHERE revision_id = $1::uuid AND status = 'active' AND auto`, revisionID); err != nil {
		return fmt.Errorf("store: drain refused split: %w", err)
	}

	message := ModelRefusal(model, fit)
	var existing string
	err := s.pool.QueryRow(ctx, `
		SELECT error FROM topologies
		 WHERE revision_id = $1::uuid AND auto AND status = 'failed'
		 ORDER BY created_at DESC LIMIT 1`, revisionID).Scan(&existing)
	if err == nil && existing == message {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: last refusal: %w", err)
	}

	var id string
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO topologies (revision_id, version, split_mode, status, auto, error)
		VALUES ($1::uuid,
		        (SELECT COALESCE(MAX(version), 0) + 1 FROM topologies WHERE revision_id = $1::uuid),
		        'layer', 'failed', true, $2)
		RETURNING id::text`, revisionID, message).Scan(&id); err != nil {
		return fmt.Errorf("store: record refusal: %w", err)
	}
	return nil
}

// LatestSplitRefusal возвращает последнюю причину, по которой группа для этой
// модели не собралась. Пустая строка значит «отказов нет».
//
// Нужна, чтобы узел и его окно знали причину: панель показывает её из
// топологии, а участнику тот же текст приходит в ответе heartbeat.
func (s *Store) LatestSplitRefusal(ctx context.Context, model string) (string, error) {
	const q = `
		SELECT t.error
		FROM topologies t
		JOIN model_revisions r ON r.id = t.revision_id
		JOIN model_catalog m ON m.id = r.model_id
		WHERE t.status = 'failed' AND t.auto AND t.error <> ''
		  AND lower(m.name) = lower($1)
		ORDER BY t.created_at DESC
		LIMIT 1`
	var text string
	err := s.pool.QueryRow(ctx, q, model).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: split refusal: %w", err)
	}
	return text, nil
}
