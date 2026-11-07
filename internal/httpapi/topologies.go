package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/private-compute/network/internal/scheduler"
	"github.com/private-compute/network/internal/store"
)

// registerTopologyRoutes подключает управление топологиями.
//
// Топология — версия размещения ревизии по узлам: кто координатор, кто какие
// слои держит. Активация вытесняет предыдущую топологию в draining (ADR 0008).
func (cfg Config) registerTopologyRoutes(mux *http.ServeMux) {
	mux.Handle("POST /admin/v1/topologies", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		var body struct {
			RevisionID    string             `json:"revision_id"`
			CoordinatorID string             `json:"coordinator_worker_id"`
			SplitMode     string             `json:"split_mode"`
			Stages        []store.StageInput `json:"stages"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.RevisionID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "revision_id is required")
			return
		}
		if len(body.Stages) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "at least one stage is required")
			return
		}
		if err := validateStages(body.Stages); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_topology", err.Error())
			return
		}

		detail, err := cfg.Store.CreateTopology(r.Context(), body.RevisionID, body.CoordinatorID, body.SplitMode, body.Stages)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create topology")
			return
		}
		writeJSON(w, http.StatusCreated, detail)
	}))

	mux.Handle("GET /admin/v1/topologies", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		topologies, err := cfg.Store.ListTopologies(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list topologies")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": topologies})
	}))

	mux.Handle("POST /admin/v1/topologies/{id}/status", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		var body struct {
			Status string `json:"status"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch body.Status {
		case "draft", "staging", "active", "draining", "retired", "failed":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown status")
			return
		}
		if err := cfg.Store.SetTopologyStatus(r.Context(), id, body.Status); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "topology not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot change status")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"topology_id": id, "status": body.Status})
	}))

	// Детали размещения: панель показывает, какие слои на каком узле.
	// Список отдаёт только шапки — этапы грузятся по требованию, чтобы таблица
	// не тянула join по всем топологиям сразу.
	mux.Handle("GET /admin/v1/topologies/{id}", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		detail, err := cfg.Store.TopologyByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "topology not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot load topology")
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))

	// Узел узнаёт, какие слои он должен держать.
	mux.Handle("GET /worker/v1/assignments", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		assignments, err := cfg.Store.WorkerAssignments(r.Context(), wp.Worker.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot load assignments")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"worker_id":   wp.Worker.ID,
			"assignments": assignments,
		})
	}))

	// Прогноз скорости одиночного запроса для топологии (ADR 0010).
	mux.Handle("GET /admin/v1/topologies/{id}/estimate", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		id := r.PathValue("id")
		detail, err := cfg.Store.TopologyByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "topology not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot load topology")
			return
		}

		manifest, err := cfg.Store.ManifestByRevision(r.Context(), detail.Topology.RevisionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot load revision")
			return
		}
		if manifest.Revision.ParamsBillions <= 0 {
			writeError(w, http.StatusConflict, "missing_params",
				"revision has no params_billions: speed estimate needs the model size")
			return
		}

		rtt := averageStageRTT(r.Context(), cfg, detail.Stages)
		estimate, err := scheduler.EstimateSpeed(scheduler.Params{
			ModelBillions: float64(manifest.Revision.ParamsBillions),
			Quantization:  manifest.Revision.Quantization,
			Stages:        stagePerf(r.Context(), cfg, detail.Stages),
			RTTMillis:     rtt,
			SplitMode:     detail.Topology.SplitMode,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_topology", err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"topology_id": id,
			"model":       manifest.Revision.ModelName,
			"version":     detail.Topology.Version,
			"split_mode":  detail.Topology.SplitMode,
			"stages":      len(detail.Stages),
			"estimate":    estimate,
			"disclaimer":  "оценка по измеренному RTT и калибровке на RTX 5060; заменяется реальным benchmark на этапе 7",
		})
	}))
}

// stagePerf строит доли слоёв и калиброванные скорости узлов топологии.
func stagePerf(ctx context.Context, cfg Config, stages []store.Stage) []scheduler.StagePerf {
	if len(stages) == 0 {
		return nil
	}

	// Доля стадии — по числу слоёв относительно суммы по топологии.
	totalLayers := 0
	for _, stage := range stages {
		totalLayers += stage.LayerEnd - stage.LayerStart + 1
	}
	if totalLayers <= 0 {
		return scheduler.Homogeneous(len(stages), 0)
	}

	workers, err := cfg.Store.ListWorkers(ctx)
	if err != nil {
		return scheduler.Homogeneous(len(stages), 0)
	}
	msPerBillion := make(map[string]float64, len(workers))
	for _, worker := range workers {
		if worker.MSPerBillion != nil && *worker.MSPerBillion > 0 {
			msPerBillion[worker.ID] = *worker.MSPerBillion
		}
	}

	out := make([]scheduler.StagePerf, 0, len(stages))
	for _, stage := range stages {
		layers := stage.LayerEnd - stage.LayerStart + 1
		out = append(out, scheduler.StagePerf{
			LayerShare:   float64(layers) / float64(totalLayers),
			MSPerBillion: msPerBillion[stage.WorkerID],
		})
	}
	return out
}

// averageStageRTT берёт средний измеренный RTT узлов топологии.
// Если замеров нет — 0, и прогноз помечается как оптимистичный.
func averageStageRTT(ctx context.Context, cfg Config, stages []store.Stage) float64 {
	if len(stages) == 0 {
		return 0
	}
	workers, err := cfg.Store.ListWorkers(ctx)
	if err != nil {
		return 0
	}
	byID := make(map[string]*int, len(workers))
	for _, worker := range workers {
		byID[worker.ID] = worker.RTTMS
	}

	total := 0
	count := 0
	for _, stage := range stages {
		if rtt, ok := byID[stage.WorkerID]; ok && rtt != nil && *rtt > 0 {
			total += *rtt
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}

// validateStages проверяет, что диапазоны слоёв непрерывны и не пересекаются.
func validateStages(stages []store.StageInput) error {
	if len(stages) == 0 {
		return errors.New("stages are empty")
	}

	sorted := make([]store.StageInput, len(stages))
	copy(sorted, stages)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LayerStart < sorted[j].LayerStart })

	if sorted[0].LayerStart != 0 {
		return fmt.Errorf("first stage must start at layer 0, got %d", sorted[0].LayerStart)
	}
	for index := 1; index < len(sorted); index++ {
		previous := sorted[index-1]
		current := sorted[index]
		if current.LayerStart != previous.LayerEnd+1 {
			return fmt.Errorf("gap or overlap between layers %d-%d and %d-%d",
				previous.LayerStart, previous.LayerEnd, current.LayerStart, current.LayerEnd)
		}
	}
	for _, stage := range sorted {
		if stage.WorkerID == "" {
			return errors.New("worker_id is required for every stage")
		}
	}
	return nil
}
