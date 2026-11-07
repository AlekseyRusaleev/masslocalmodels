package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/private-compute/network/internal/store"
)

// registerModelRoutes подключает каталог моделей и манифест артефактов.
//
// Администратор заводит модель, ревизию и артефакты; узел получает манифест и
// скачивает файлы сам по url — координатор байты не передаёт (ADR 0008 §5.4).
func (cfg Config) registerModelRoutes(mux *http.ServeMux) {
	mux.Handle("POST /admin/v1/models", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		var body struct {
			Name           string  `json:"name"`
			DisplayName    string  `json:"display_name"`
			TaskType       string  `json:"task_type"`
			Source         string  `json:"source"`
			Revision       string  `json:"revision"`
			License        string  `json:"license"`
			LicenseOK      bool    `json:"license_checked"`
			Runtime        string  `json:"runtime"`
			Quantization   string  `json:"quantization"`
			ParamsBillions float32 `json:"params_billions"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
			return
		}
		if body.TaskType == "" {
			body.TaskType = "llm"
		}
		if body.Runtime == "" {
			body.Runtime = "llama.cpp"
		}

		model, err := cfg.Store.EnsureModel(r.Context(), body.Name, body.DisplayName, body.TaskType)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot upsert model")
			return
		}
		revision, err := cfg.Store.CreateRevision(r.Context(), model.ID, body.Source, body.Revision,
			body.License, body.LicenseOK, body.Runtime, body.Quantization, body.ParamsBillions)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create revision")
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"model":    model,
			"revision": revision,
			"warning":  "artifacts must be registered before a node can fetch the revision",
		})
	}))

	mux.Handle("POST /admin/v1/models/{revisionID}/artifacts", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		revisionID := r.PathValue("revisionID")
		var body struct {
			Artifacts []struct {
				Name       string `json:"name"`
				URL        string `json:"url"`
				Size       int64  `json:"size_bytes"`
				SHA256     string `json:"sha256"`
				LayerStart *int   `json:"layer_start"`
				LayerEnd   *int   `json:"layer_end"`
			} `json:"artifacts"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if len(body.Artifacts) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "artifacts are required")
			return
		}

		saved := make([]store.Artifact, 0, len(body.Artifacts))
		for index, item := range body.Artifacts {
			if item.Name == "" || item.URL == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "artifact name and url are required")
				return
			}
			if item.SHA256 != "" && len(item.SHA256) != 64 {
				writeError(w, http.StatusBadRequest, "invalid_request", "sha256 must be 64 hex chars")
				return
			}
			artifact, err := cfg.Store.AddArtifact(r.Context(), revisionID, item.Name, item.URL,
				strings.ToLower(item.SHA256), item.Size, index, item.LayerStart, item.LayerEnd)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "cannot store artifact")
				return
			}
			saved = append(saved, artifact)
		}

		writeJSON(w, http.StatusCreated, map[string]any{"object": "list", "data": saved})
	}))

	mux.Handle("GET /admin/v1/models", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		revisions, err := cfg.Store.ListRevisions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list revisions")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": revisions})
	}))

	mux.Handle("POST /admin/v1/models/{revisionID}/status", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		revisionID := r.PathValue("revisionID")
		var body struct {
			Status string `json:"status"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		switch body.Status {
		case "draft", "ready", "active", "retired":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown status")
			return
		}
		if err := cfg.Store.SetRevisionStatus(r.Context(), revisionID, body.Status); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "revision not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot change status")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revision_id": revisionID, "status": body.Status})
	}))

	// Подпись манифеста: закрепляет файлы и их хэши ключом платформы.
	// Пока манифест не подписан, аттестация весов не считается подтверждённой.
	mux.Handle("POST /admin/v1/models/{revisionID}/sign", cfg.requireAuth("admin", func(w http.ResponseWriter, r *http.Request, _ principal) {
		revisionID := r.PathValue("revisionID")
		signature, err := cfg.Store.SignRevision(r.Context(), cfg.Pepper, revisionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "revision not found")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"revision_id": revisionID,
			"signature":   signature,
			"signed_at":   time.Now().UTC(),
		})
	}))

	// Манифест для узла: device token, только чтение.
	// Возвращает только те артефакты, которые пересекаются с этапом узла в топологии.
	mux.Handle("GET /worker/v1/artifacts/manifest", cfg.requireWorker(func(w http.ResponseWriter, r *http.Request, wp workerPrincipal) {
		revisionID := r.URL.Query().Get("revision")
		if revisionID == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "revision query parameter is required")
			return
		}
		manifest, err := cfg.Store.ManifestForWorker(r.Context(), revisionID, wp.Worker.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "revision not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot build manifest")
			return
		}
		if len(manifest.Artifacts) == 0 {
			writeError(w, http.StatusConflict, "empty_revision",
				"no artifacts for this worker: register artifacts or add the worker to a topology")
			return
		}
		writeJSON(w, http.StatusOK, manifest)
	}))
}
