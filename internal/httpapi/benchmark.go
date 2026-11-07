package httpapi

import (
	"net/http"
)

// registerBenchmarkRoutes подключает калибровку узла.
//
// Агент прогоняет короткий замер декода на своём железе и присылает результат.
// Scheduler нормирует его на размер эталонной модели и использует вместо общей
// константы «1.79 мс на 1 млрд параметров» (план §14.2: калибровка до начисления CU).
func (cfg Config) registerBenchmarkRoutes(mux *http.ServeMux) {
	mux.Handle("POST /worker/v1/benchmark", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		var body struct {
			Model           string  `json:"model"`
			ModelBillions   float64 `json:"model_billions"`
			Quantization    string  `json:"quantization"`
			TokensPerSecond float64 `json:"tokens_per_second"`
			GeneratedTokens int     `json:"generated_tokens"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.ModelBillions <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "model_billions must be positive")
			return
		}
		if body.TokensPerSecond <= 0 || body.TokensPerSecond > 10000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "tokens_per_second is out of range")
			return
		}

		if err := cfg.Store.SaveBenchmark(r.Context(), wp.Worker.ID, body.ModelBillions,
			body.Quantization, body.TokensPerSecond); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot save benchmark")
			return
		}

		msPerBillion := (1000 / body.TokensPerSecond) / body.ModelBillions
		writeJSON(w, http.StatusOK, map[string]any{
			"worker_id":         wp.Worker.ID,
			"model":             body.Model,
			"tokens_per_second": body.TokensPerSecond,
			"ms_per_billion":    msPerBillion,
			"note":              "калибровка применена к прогнозу скорости",
		})
	}))
}
