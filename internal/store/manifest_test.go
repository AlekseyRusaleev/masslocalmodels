package store

import (
	"strings"
	"testing"
	"time"
)

// Канонический текст манифеста должен быть устойчив: от него зависит подпись.
func TestCanonicalManifestIsStable(t *testing.T) {
	revision := Revision{ModelName: "Qwen3:8B", RevisionRef: "abc123", Quantization: "Q4_K_M", Runtime: "ollama"}
	artifacts := []Artifact{
		{Name: "model.gguf", SHA256: strings.Repeat("a", 64), Size: 100},
	}
	text := CanonicalManifest(revision, artifacts)

	for _, want := range []string{"model=qwen3:8b", "revision=abc123", "quantization=Q4_K_M", "file=model.gguf sha256=" + strings.Repeat("a", 64)} {
		if !strings.Contains(text, want) {
			t.Fatalf("в каноническом тексте нет %q:\n%s", want, text)
		}
	}

	// Имя модели приводится к нижнему регистру: подпись не должна зависеть от
	// того, как администратор набрал имя.
	upper := CanonicalManifest(Revision{ModelName: "QWEN3:8B", RevisionRef: "abc123", Quantization: "Q4_K_M", Runtime: "ollama"}, artifacts)
	if upper != text {
		t.Fatalf("подпись зависит от регистра имени модели:\n%s\n%s", text, upper)
	}
}

// Любое изменение хэша файла ломает подпись — ради этого она и нужна.
func TestManifestSignatureCatchesTampering(t *testing.T) {
	pepper := []byte("pepper-for-tests")
	revision := Revision{ModelName: "qwen3:8b", RevisionRef: "abc", Runtime: "ollama"}
	original := []Artifact{{Name: "model.gguf", SHA256: strings.Repeat("a", 64), Size: 1}}
	signature := SignManifestText(pepper, CanonicalManifest(revision, original))

	if !manifestSignatureValid(pepper, CanonicalManifest(revision, original), signature) {
		t.Fatal("подпись не подтверждает собственный манифест")
	}

	tampered := []Artifact{{Name: "model.gguf", SHA256: strings.Repeat("b", 64), Size: 1}}
	if manifestSignatureValid(pepper, CanonicalManifest(revision, tampered), signature) {
		t.Fatal("подмена хэша файла не обнаружена")
	}

	added := append(append([]Artifact{}, original...), Artifact{Name: "extra.gguf", SHA256: strings.Repeat("c", 64), Size: 1})
	if manifestSignatureValid(pepper, CanonicalManifest(revision, added), signature) {
		t.Fatal("добавление файла не обнаружено")
	}

	if manifestSignatureValid([]byte("another-pepper"), CanonicalManifest(revision, original), signature) {
		t.Fatal("подпись не зависит от ключа платформы")
	}
	if manifestSignatureValid(pepper, CanonicalManifest(revision, original), "") {
		t.Fatal("пустая подпись принята")
	}
}

// Ревизия без подписи не может подтвердить веса — это отдельный вердикт.
func TestVerdictStrings(t *testing.T) {
	verdicts := []string{AttestationVerified, AttestationNoManifest, AttestationUnsigned, AttestationMismatch, AttestationNotReported}
	seen := map[string]bool{}
	for _, verdict := range verdicts {
		if verdict == "" || seen[verdict] {
			t.Fatalf("пустой или повторяющийся вердикт: %q", verdict)
		}
		seen[verdict] = true
	}
	if !(Attestation{}.Verdict != AttestationVerified) || (Attestation{Verdict: AttestationVerified}).Verified() != true {
		t.Fatal("Verified() не различает подтверждённый и неподтверждённый вердикт")
	}
	_ = time.Now
}
