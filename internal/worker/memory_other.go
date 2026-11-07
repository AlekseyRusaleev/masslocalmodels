//go:build !windows

package worker

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// MemoryStatus возвращает свободную физическую память и свободный запас
// фиксации (commit) в байтах.
//
// На Linux физическая память — MemAvailable (оценка ядра с учётом кэша,
// который можно отдать), запас фиксации — CommitLimit минус Committed_AS.
func MemoryStatus() (freePhysBytes, freeCommitBytes uint64, err error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, convErr := strconv.ParseUint(fields[1], 10, 64)
		if convErr != nil {
			continue
		}
		values[key] = value * 1024 // meminfo в килобайтах
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}

	freePhysBytes = values["MemAvailable"]
	limit := values["CommitLimit"]
	committed := values["Committed_AS"]
	if limit > committed {
		freeCommitBytes = limit - committed
	}
	return freePhysBytes, freeCommitBytes, nil
}
