package store

import (
	"context"
	"fmt"
	"strings"
)

// Attestation — итог сверки весов узла с подписанным манифестом каталога.
type Attestation struct {
	// Verdict — одна из констант Attestation*.
	Verdict string
	// Expected — ожидаемый digest (для однофайловой ревизии).
	Expected string
	// Observed — digest, который сообщил узел.
	Observed string
	// Detail — человеческое объяснение расхождения.
	Detail string
}

// Verified сообщает, что веса подтверждены манифестом.
func (a Attestation) Verified() bool { return a.Verdict == AttestationVerified }

// NormalizeDigest приводит digest к сравнимому виду: Ollama отдаёт его с
// префиксом «sha256:», а в манифесте хэш лежит без префикса.
func NormalizeDigest(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "sha256:")
	return value
}

// DigestsMatch сравнивает хэши весов после нормализации.
//
// Пустой хэш не совпадает ни с чем: «узел не сообщил» не должно выглядеть как
// «совпало с манифестом».
func DigestsMatch(expected, observed string) bool {
	left, right := NormalizeDigest(expected), NormalizeDigest(observed)
	if left == "" || right == "" {
		return false
	}
	return left == right
}

// VerifyAttestation сверяет веса узла с манифестом модели.
//
// Порядок проверок важен: сначала наличие и подпись манифеста (иначе сверять
// нечего), затем сам digest. Для ревизии из нескольких файлов нужен набор
// хэшей по файлам: один digest модели не доказывает набор шардов.
func (s *Store) VerifyAttestation(ctx context.Context, pepper []byte, model, observed string, files map[string]string) (Attestation, error) {
	manifest, found, err := s.ManifestAttestation(ctx, pepper, model)
	if err != nil {
		return Attestation{}, err
	}
	if !found {
		// Наблюдаемый digest возвращаем и здесь: панель показывает, что узел
		// загрузил, даже когда сверять не с чем.
		return Attestation{
			Verdict:  AttestationNoManifest,
			Observed: NormalizeDigest(observed),
			Detail:   "no revision for this model in the catalog",
		}, nil
	}

	// Наблюдаемый digest сохраняем в любом вердикте: панель должна показывать,
	// что именно загрузил узел, даже когда сверять не с чем.
	attestation := Attestation{Expected: manifest.Single, Observed: NormalizeDigest(observed)}

	if manifest.Signature == "" {
		attestation.Verdict = AttestationUnsigned
		attestation.Detail = "manifest is not signed by the platform"
		return attestation, nil
	}
	if !manifest.SignatureValid {
		attestation.Verdict = AttestationMismatch
		attestation.Detail = "manifest signature does not match its content"
		return attestation, nil
	}

	if manifest.Single != "" {
		if attestation.Observed == "" {
			attestation.Verdict = AttestationNotReported
			attestation.Detail = "worker did not report the loaded weights digest"
			return attestation, nil
		}
		if DigestsMatch(manifest.Single, attestation.Observed) {
			attestation.Verdict = AttestationVerified
			return attestation, nil
		}
		attestation.Verdict = AttestationMismatch
		attestation.Detail = fmt.Sprintf("manifest expects %s, worker loaded %s", manifest.Single, attestation.Observed)
		return attestation, nil
	}

	// Ревизия из нескольких файлов: нужен отчёт по каждому файлу.
	if len(files) == 0 {
		attestation.Verdict = AttestationNotReported
		attestation.Detail = fmt.Sprintf("manifest has %d files; worker reported no per-file hashes", len(manifest.Files))
		return attestation, nil
	}
	missing := make([]string, 0, len(manifest.Files))
	wrong := make([]string, 0, len(manifest.Files))
	for name, expected := range manifest.Files {
		got, ok := files[name]
		switch {
		case !ok:
			missing = append(missing, name)
		case !DigestsMatch(expected, got):
			wrong = append(wrong, name)
		}
	}
	if len(missing) > 0 || len(wrong) > 0 {
		attestation.Verdict = AttestationMismatch
		attestation.Detail = fmt.Sprintf("files do not match the manifest: missing %d, mismatched %d",
			len(missing), len(wrong))
		return attestation, nil
	}
	attestation.Verdict = AttestationVerified
	return attestation, nil
}

// SaveModelDigests сохраняет, какие веса держит узел.
//
// Устаревшие записи удаляются: список из heartbeat — это полная картина, а не
// приращение. Иначе удалённая модель навсегда осталась бы в панели.
func (s *Store) SaveModelDigests(ctx context.Context, workerID string, digests map[string]string, selfVerified map[string]bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM worker_model_digests WHERE worker_id = $1::uuid`, workerID); err != nil {
		return fmt.Errorf("store: clear model digests: %w", err)
	}
	for model, digest := range digests {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO worker_model_digests (worker_id, model, digest, self_verified)
			 VALUES ($1::uuid, $2, $3, $4)
			 ON CONFLICT (worker_id, model) DO UPDATE
			   SET digest = EXCLUDED.digest, self_verified = EXCLUDED.self_verified, observed_at = now()`,
			workerID, model, NormalizeDigest(digest), selfVerified[model]); err != nil {
			return fmt.Errorf("store: save model digest: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ModelDigests отдаёт digest'ы моделей узла и отметки самопроверки.
func (s *Store) ModelDigests(ctx context.Context, workerID string) (map[string]string, map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT model, digest, self_verified FROM worker_model_digests WHERE worker_id = $1::uuid`, workerID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: model digests: %w", err)
	}
	defer rows.Close()

	digests := make(map[string]string, 4)
	verified := make(map[string]bool, 4)
	for rows.Next() {
		var model, digest string
		var selfVerified bool
		if err := rows.Scan(&model, &digest, &selfVerified); err != nil {
			return nil, nil, fmt.Errorf("store: scan model digest: %w", err)
		}
		digests[model] = digest
		verified[model] = selfVerified
	}
	return digests, verified, rows.Err()
}

// WorkerAttestation — по каждой обслуживаемой модели узла: совпадает ли digest
// с подписанным манифестом. Панель показывает это в карточке узла.
type WorkerAttestation struct {
	Model        string `json:"model"`
	Digest       string `json:"digest"`
	Expected     string `json:"expected,omitempty"`
	Verified     bool   `json:"verified"`
	SelfVerified bool   `json:"self_verified"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason,omitempty"`
}

// AttestationsForWorker собирает сверку по всем моделям узла.
func (s *Store) AttestationsForWorker(ctx context.Context, pepper []byte, workerID string) ([]WorkerAttestation, error) {
	digests, selfVerified, err := s.ModelDigests(ctx, workerID)
	if err != nil {
		return nil, err
	}

	out := make([]WorkerAttestation, 0, len(digests))
	for model, digest := range digests {
		attestation, err := s.VerifyAttestation(ctx, pepper, model, digest, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, WorkerAttestation{
			Model:        model,
			Digest:       attestation.Observed,
			Expected:     attestation.Expected,
			Verified:     attestation.Verified(),
			SelfVerified: selfVerified[model],
			Verdict:      attestation.Verdict,
			Reason:       attestation.Detail,
		})
	}
	return out, nil
}

// SetJobAttestation записывает итог сверки рядом с заданием.
func (s *Store) SetJobAttestation(ctx context.Context, jobID, verdict string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE jobs SET attestation = $2 WHERE id = $1::uuid`, jobID, verdict); err != nil {
		return fmt.Errorf("store: set job attestation: %w", err)
	}
	return nil
}

// SetJobVerified отмечает, прошло ли задание сверку весов.
func (s *Store) SetJobVerified(ctx context.Context, jobID string, verified bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE jobs SET digest_verified = $2 WHERE id = $1::uuid`, jobID, verified)
	if err != nil {
		return fmt.Errorf("store: set job verified: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
