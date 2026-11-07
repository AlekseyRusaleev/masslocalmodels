//go:build linux

package worker

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// releaseByPath останавливает процессы, чей исполняемый файл подходит под match.
// Чужие программы не трогает: фильтр тот же, что и на Windows.
func releaseByPath(match func(string) bool) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	n := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		link, err := os.Readlink("/proc/" + entry.Name() + "/exe")
		if err != nil || !match(link) {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if proc.Signal(syscall.SIGKILL) == nil {
			n++
		}
	}
	return n
}

func stopOllamaModelServers() int {
	return releaseByPath(func(path string) bool {
		return holdsGPU(path) && strings.Contains(strings.ToLower(path), "ollama")
	})
}

func ReleaseLocalGPU() int {
	return releaseByPath(holdsGPU)
}
