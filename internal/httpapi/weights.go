package httpapi

import (
	"context"
	"errors"

	"github.com/private-compute/network/internal/store"
	"github.com/private-compute/network/internal/swarm"
	"github.com/private-compute/network/internal/worker"
)

// weightDiskReserve — сколько свободного места оставляем владельцу компьютера.
// Доля весов не должна забивать диск под ноль.
const weightDiskReserve int64 = 2 << 30

// weightPlan считает, какие файлы этот компьютер должен держать прямо сейчас.
//
// Каталога ещё может не быть: тогда план пустой, и узел ничего не скачивает.
// Ошибка чтения не должна рвать heartbeat — вызывающий пишет её в журнал.
func (cfg Config) weightPlan(ctx context.Context, workerID string) (*worker.WeightPlan, error) {
	manifest, err := cfg.Store.ActiveWeightCatalog(ctx, swarm.RetiredModel)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(manifest.Artifacts) == 0 {
		return nil, nil
	}
	seeders, err := cfg.Store.OnlineSeeders(ctx, servingWindow)
	if err != nil {
		return nil, err
	}

	pieces := make([]swarm.Piece, 0, len(manifest.Artifacts))
	files := make(map[string]worker.WeightFile, len(manifest.Artifacts))
	sizeOf := map[string]int64{}
	for _, artifact := range manifest.Artifacts {
		pieces = append(pieces, swarm.Piece{
			ID:     artifact.Name,
			Bytes:  artifact.Size,
			Shared: artifact.LayerStart == nil,
		})
		sizeOf[artifact.Name] = artifact.Size
		files[artifact.Name] = worker.WeightFile{
			Name:   artifact.Name,
			URL:    artifact.URL,
			SHA256: artifact.SHA256,
			Bytes:  artifact.Size,
		}
	}

	peers := make([]swarm.Peer, 0, len(seeders))
	for _, seeder := range seeders {
		var heldBytes int64
		for _, name := range seeder.Held {
			if size, ok := sizeOf[name]; ok && size > 0 {
				heldBytes += size
			}
		}
		budget := heldBytes
		if seeder.DiskFreeBytes != nil {
			room := *seeder.DiskFreeBytes - weightDiskReserve
			if room > 0 {
				budget += room
			}
		}
		peers = append(peers, swarm.Peer{ID: seeder.ID, Budget: budget, Have: seeder.Held})
	}

	placed := swarm.Place(pieces, peers, swarm.Options{Replicas: swarm.DefaultReplicas})
	decision, ok := swarm.DecisionFor(placed, workerID)
	if !ok {
		return nil, nil
	}
	plan := &worker.WeightPlan{
		RevisionID: manifest.Revision.ID,
		Model:      manifest.Revision.ModelName,
		Keep:       decision.Keep,
		Release:    decision.Release,
		Missing:    placed.Missing,
		Short:      placed.Short,
	}
	for _, name := range decision.Fetch {
		file, ok := files[name]
		if !ok {
			continue
		}
		plan.Fetch = append(plan.Fetch, file)
	}
	return plan, nil
}

func (cfg Config) logWeightPlanError(err error) {
	if err == nil || cfg.Log == nil {
		return
	}
	cfg.Log.Warn("weight plan skipped", "err", err)
}
