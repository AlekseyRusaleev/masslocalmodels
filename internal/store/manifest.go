package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Итоги аттестации. Строки попадают в jobs.attestation и в панель, поэтому они
// фиксированы и переводимы на человеческий язык в одном месте.
const (
	// AttestationVerified — digest узла совпал с подписанным манифестом.
	AttestationVerified = "verified"
	// AttestationNoManifest — в каталоге нет ревизии этой модели.
	AttestationNoManifest = "no_manifest"
	// AttestationUnsigned — ревизия есть, но манифест не подписан.
	AttestationUnsigned = "unsigned_manifest"
	// AttestationMismatch — хэши не совпали: узел обслужил другие веса.
	AttestationMismatch = "mismatch"
	// AttestationNotReported — узел не сообщил digest.
	AttestationNotReported = "not_reported"
)

// CanonicalManifest — текст манифеста, который подписывается.
//
// Порядок строк фиксирован: ревизия, затем файлы по имени. Подпись считается по
// этому тексту, поэтому любое изменение sha256 или набора файлов её ломает —
// именно это и нужно.
func CanonicalManifest(revision Revision, artifacts []Artifact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model=%s\n", strings.ToLower(strings.TrimSpace(revision.ModelName)))
	fmt.Fprintf(&b, "revision=%s\n", strings.TrimSpace(revision.RevisionRef))
	fmt.Fprintf(&b, "quantization=%s\n", strings.TrimSpace(revision.Quantization))
	fmt.Fprintf(&b, "runtime=%s\n", strings.TrimSpace(revision.Runtime))
	for _, artifact := range artifacts {
		fmt.Fprintf(&b, "file=%s sha256=%s size=%d\n",
			strings.TrimSpace(artifact.Name), NormalizeDigest(artifact.SHA256), artifact.Size)
	}
	return b.String()
}

// SignManifestText считает подпись манифеста ключом платформы.
func SignManifestText(pepper []byte, text string) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(text))
	return hex.EncodeToString(mac.Sum(nil))
}

// manifestSignatureValid сравнивает подпись за постоянное время.
func manifestSignatureValid(pepper []byte, text, signature string) bool {
	expected := SignManifestText(pepper, text)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// SignRevision закрепляет текущее содержимое манифеста подписью.
//
// Пересчитывается по фактическим данным ревизии и файлов: подписать «что-то
// другое» нельзя, и после любой правки файлов подпись нужно ставить заново.
func (s *Store) SignRevision(ctx context.Context, pepper []byte, revisionID string) (string, error) {
	manifest, err := s.ManifestByRevision(ctx, revisionID)
	if err != nil {
		return "", err
	}
	if len(manifest.Artifacts) == 0 {
		return "", fmt.Errorf("store: cannot sign a revision without files")
	}
	signature := SignManifestText(pepper, CanonicalManifest(manifest.Revision, manifest.Artifacts))

	tag, err := s.pool.Exec(ctx,
		`UPDATE model_revisions SET manifest_signature = $2, manifest_signed_at = now() WHERE id = $1::uuid`,
		revisionID, signature)
	if err != nil {
		return "", fmt.Errorf("store: sign revision: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return signature, nil
}

// RevisionAttestation — всё, что нужно, чтобы сверить веса узла с манифестом.
type RevisionAttestation struct {
	// Signature — подпись, сохранённая у ревизии (пусто — не подписана).
	Signature string
	// SignatureValid — подпись совпадает с текущим содержимым манифеста.
	SignatureValid bool
	// Files — sha256 по именам файлов ревизии.
	Files map[string]string
	// Single — digest единственного файла, если файл один.
	Single string
}

// ManifestAttestation собирает ожидаемые хэши и проверяет подпись манифеста.
func (s *Store) ManifestAttestation(ctx context.Context, pepper []byte, model string) (RevisionAttestation, bool, error) {
	manifest, err := s.ManifestForModel(ctx, model)
	if err != nil {
		if err == ErrNotFound {
			return RevisionAttestation{}, false, nil
		}
		return RevisionAttestation{}, false, err
	}

	files := make(map[string]string, len(manifest.Artifacts))
	digests := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		digest := NormalizeDigest(artifact.SHA256)
		if digest == "" {
			continue
		}
		files[artifact.Name] = digest
		digests = append(digests, digest)
	}

	result := RevisionAttestation{
		Signature: manifest.Revision.ManifestSignature,
		SignatureValid: manifest.Revision.ManifestSignature != "" &&
			manifestSignatureValid(pepper, CanonicalManifest(manifest.Revision, manifest.Artifacts), manifest.Revision.ManifestSignature),
		Files: files,
	}
	if len(digests) == 1 {
		result.Single = digests[0]
	}
	return result, true, nil
}
