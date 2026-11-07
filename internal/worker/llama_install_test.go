package worker

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestLlamaCUDAFamily(t *testing.T) {
	if got := llamaCUDAFamily("13.0"); got != "13.4" {
		t.Fatalf("CUDA 13.0 → %s", got)
	}
	if got := llamaCUDAFamily("12.8"); got != "12.4" {
		t.Fatalf("CUDA 12.8 → %s", got)
	}
	if got := llamaCUDAFamily(""); got != "13.4" {
		t.Fatalf("пустая версия → %s", got)
	}
}

func TestExtractZipKeepsFilesInsideTheDestination(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "llama.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("build/llama-server.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("server")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()

	dest := filepath.Join(dir, "out")
	if err := extractZip(archive, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "build", "llama-server.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractZipRejectsAPathThatEscapes(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	if _, err := writer.Create("../outside.txt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()

	if err := extractZip(archive, filepath.Join(dir, "out")); err == nil {
		t.Fatal("архив с выходом наружу должен быть отклонён")
	}
}

func TestLlamaRuntimeReadyNeedsCUDAOnWindows(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{executableName("llama-server"), executableName("ggml-rpc-server")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if filepath.Separator == '\\' && llamaRuntimeReady(dir) {
		t.Fatal("без cudart каталог на Windows не готов")
	}
	if err := os.WriteFile(filepath.Join(dir, "cudart64_13.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !llamaRuntimeReady(dir) {
		t.Fatal("оба файла и CUDA на месте")
	}
}
