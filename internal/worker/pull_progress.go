package worker

import (
	"os"
	"path/filepath"
	"strings"
)

// ModelDownloadProgress — сколько байт новой модели уже записано на диск.
//
// Ollama заранее создаёт файл полного размера, поэтому обычная длина файла
// сразу равна всей модели и полоса загрузки не двигается. Здесь считается
// занятое место: оно растёт, пока байты реально ложатся на диск.
func ModelDownloadProgress() (done, total int64, active bool) {
	dir := filepath.Join(ollamaModelDir(), "blobs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, false
	}
	var path string
	var logical int64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "-partial") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() <= logical {
			continue
		}
		path = filepath.Join(dir, name)
		logical = info.Size()
	}
	// Мелкий служебный файл не является весами модели.
	if path == "" || logical < 8<<20 {
		return 0, 0, false
	}
	written := allocatedFileBytes(path)
	if written <= 0 || written > logical {
		written = logical
	}
	return written, logical, modelPullActive(written, logical)
}

// modelPullActive отличает ещё идущую запись от уже дописанного файла.
// Небольшой запас в конце не держит полосу на 99%, когда файл уже на месте.
func modelPullActive(written, logical int64) bool {
	return logical >= 8<<20 && written+1<<20 < logical
}
